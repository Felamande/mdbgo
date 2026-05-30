package puredb

import (
	"fmt"
	"strconv"
)

// kkdToProps parses KKD/MR2 binary property data and returns a slice of Properties.
func kkdToProps(mdb *MdbHandle, data []byte, length int) []*Properties {
	if length < 4 {
		return nil
	}

	// Check magic
	magic := string(data[:4])
	if magic != "KKD " && magic != "MR2 " {
		return nil
	}

	var result []*Properties
	var names []string

	pos := 4
	for pos < length {
		recordLen := int(GetInt32(data, pos))
		recordType := int(GetInt16(data, pos+4))

		if recordLen <= 0 || pos+recordLen > length {
			break
		}

		switch recordType {
		case 0x80:
			// Property name list
			names = readPropsList(mdb, data[pos+6:pos+recordLen], recordLen-6)

		case 0x00, 0x01, 0x02:
			// Property block
			if names == nil {
				pos += recordLen
				continue
			}
			props := readProps(mdb, names, data[pos+6:pos+recordLen], recordLen-6)
			result = append(result, props)

		default:
			// Unknown record type, skip
		}
		pos += recordLen
	}

	return result
}

// readPropsList reads the property name list from a KKD buffer.
func readPropsList(mdb *MdbHandle, data []byte, length int) []string {
	var names []string
	pos := 0
	for pos < length {
		recordLen := int(GetInt16(data, pos))
		pos += 2
		if recordLen <= 0 || pos+recordLen > length {
			break
		}
		name := UnicodeToUTF8(data[pos:pos+recordLen], mdb.IsJet4())
		names = append(names, name)
		pos += recordLen
	}
	return names
}

// readProps reads a single property block.
func readProps(mdb *MdbHandle, names []string, data []byte, length int) *Properties {
	if length < 6 {
		return nil
	}

	pos := 0
	recordLen := int(GetInt16(data, pos))
	pos += 4 // skip recordLen + 2 more bytes
	nameLen := int(GetInt16(data, pos))
	pos += 2

	props := &Properties{
		Hash: make(map[string]string),
	}

	if nameLen > 0 && pos+nameLen <= length {
		props.Name = UnicodeToUTF8(data[pos:pos+nameLen], mdb.IsJet4())
	}
	pos += nameLen

	for pos < length {
		recordLen = int(GetInt16(data, pos))
		if recordLen <= 0 || pos+8 > length {
			break
		}
		dtype := int(data[pos+3])
		elem := int(GetInt16(data, pos+4))
		dsize := int(GetInt16(data, pos+6))

		if elem >= len(names) {
			break
		}
		if dsize < 0 || pos+8+dsize > length {
			break
		}

		name := names[elem]

		var valueStr string
		switch {
		case dtype == TypeBool:
			if data[pos+8] != 0 {
				valueStr = "yes"
			} else {
				valueStr = "no"
			}
		case dtype == TypeBinary || dtype == TypeOLE:
			valueStr = fmt.Sprintf("(binary data of length %d)", dsize)
		case dtype == TypeMemo:
			valueStr = UnicodeToUTF8(data[pos+8:pos+8+dsize], mdb.IsJet4())
		default:
			// Use colToString-like conversion
			valueStr = valueToString(mdb, dtype, data, pos+8, dsize)
		}

		props.Hash[name] = valueStr
		pos += recordLen
	}

	return props
}

// valueToString converts a typed value to its string representation.
func valueToString(mdb *MdbHandle, dtype int, buf []byte, start, size int) string {
	switch dtype {
	case TypeBool:
		if buf[start] != 0 {
			return "1"
		}
		return "0"
	case TypeByte:
		return strconv.FormatInt(int64(buf[start]), 10)
	case TypeInt:
		return strconv.FormatInt(int64(GetInt16(buf, start)), 10)
	case TypeLongInt, TypeComplex:
		return strconv.FormatInt(int64(GetInt32(buf, start)), 10)
	case TypeFloat:
		return strconv.FormatFloat(float64(GetSingle(buf, start)), 'g', 8, 32)
	case TypeDouble:
		return strconv.FormatFloat(GetDouble(buf, start), 'g', 16, 64)
	case TypeText:
		return UnicodeToUTF8(buf[start:start+size], mdb.IsJet4())
	case TypeRepID:
		if size == 16 {
			return uuidToString(buf, start)
		}
		return fmt.Sprintf("%x", buf[start:start+size])
	default:
		return fmt.Sprintf("%s", buf[start:start+size])
	}
}

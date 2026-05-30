package puredb

// ColFixedSize returns the fixed byte size for a column type, or -1 if variable.
func ColFixedSize(colType int) int {
	switch colType {
	case TypeBool:
		return 1
	case TypeByte:
		return -1
	case TypeInt:
		return 2
	case TypeLongInt, TypeComplex:
		return 4
	case TypeFloat:
		return 4
	case TypeDouble:
		return 8
	case TypeText:
		return -1
	case TypeDateTime:
		return 4 // stored as 8 bytes (double), but this is an error
	case TypeBinary:
		return -1
	case TypeMemo:
		return -1
	case TypeMoney:
		return 8
	}
	return 0
}

// ColDispSize returns the display size for a column type.
func ColDispSize(colType, colSize int) int {
	switch colType {
	case TypeBool:
		return 1
	case TypeByte:
		return 4
	case TypeInt:
		return 6
	case TypeLongInt, TypeComplex:
		return 11
	case TypeFloat:
		return 10
	case TypeDouble:
		return 10
	case TypeText:
		return colSize
	case TypeDateTime:
		return 20
	case TypeMemo:
		return 64000
	case TypeMoney:
		return 21
	}
	return 0
}

// colBindSize returns the buffer size needed for binding a column's string value.
func colBindSize(col *MdbColumn) int {
	switch col.ColType {
	case TypeBool:
		return 2
	case TypeByte:
		return 4
	case TypeInt:
		return 7
	case TypeLongInt, TypeComplex:
		return 12
	case TypeFloat:
		return 20
	case TypeDouble:
		return 25
	case TypeDateTime:
		return 20
	case TypeMoney:
		return 30
	case TypeRepID:
		return 40
	case TypeNumeric:
		return 30
	case TypeText:
		n := col.ColSize * 3 // UCS-2 to UTF-8 can triple for CJK
		if n < 64 { n = 64 }
		if n > 8192 { n = 8192 }
		return n
	case TypeMemo:
		n := col.ColSize
		if n < 4096 { n = 4096 }
		if n > 65536 { n = 65536 }
		return n
	case TypeBinary, TypeOLE:
		n := col.ColSize + MemoOverhead
		if n < 64 { n = 64 }
		return n
	default:
		return 256
	}
}

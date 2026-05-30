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

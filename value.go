package set

import (
	"fmt"
	"reflect"

	"github.com/nofeaturesonlybugs/errors"
)

const (
	error_V_NotAssignable = "Original type passed to V() not assignable; pass an address."
)

// V returns a new Value.
//
// Memory is possibly created when calling this function:
//	// No memory is created because b is a local variable and we pass its address.
//	var b bool
//	v := set.V(&b)
//
//	// No memory is created because bp points at an existing local variable and we pass the pointer bp.
//	var b bool
//	bp := &b
//	v := set.V(bp)
//
//	// Memory is created because the local variable is an unallocated pointer AND we pass its address.
//	var *bp bool
//	v := set.V(&bp)
//	// bp now contains allocated memory.
func V(arg interface{}) *Value {
	var v reflect.Value
	//
	rv := &Value{}
	rv.original = arg
	//
	rv.methodAppend = rv.appendUnsupported
	rv.methodFields = rv.fieldsUnsupported
	rv.methodFieldsByTag = rv.fieldsByTagUnsupported
	rv.methodNewElem = rv.newElemUnsupported
	rv.methodZero = rv.zeroUnsupported
	//
	if argReflectValue, ok := arg.(reflect.Value); ok {
		v = argReflectValue
	} else {
		v = reflect.ValueOf(arg)
	}
	rv.WriteValue, rv.TypeInfo, rv.CanWrite = Writable(v)
	rv.TopValue = v

	if rv.IsMap || rv.IsSlice {
		// fmt.Printf("Adding support for...NewElem()\n") //TODO RM
		rv.ElemTypeInfo = TypeCache.StatType(rv.ElemType)
		rv.methodNewElem = rv.newElemSupported
	}
	//
	if rv.CanWrite {
		rv.methodZero = rv.zeroSupported
	}
	//
	if rv.IsSlice {
		// fmt.Printf("Adding support for...Append()\n") // TODO RM
		rv.methodAppend = rv.appendSupported
	}
	if rv.IsStruct {
		// fmt.Printf("Adding support for...Fields()\n")      //TODO RM
		// fmt.Printf("Adding support for...FieldsByTag()\n") //TODO RM
		rv.methodFields = rv.fieldsSupported
		rv.methodFieldsByTag = rv.fieldsByTagSupported
	}
	//
	return rv
}

// Value wraps around a Go variable and performs magic.
type Value struct {
	// TypeInfo describes the type T in WriteValue.  When the value is created with a pointer P
	// this TypeInfo will describe the final type at the end of the pointer chain.
	//
	// To conserve memory and maintain speed this TypeInfo object may be shared with
	// other *Value instances.  Altering the members within TypeInfo will most likely
	// crash your program with a panic.
	//
	// Treat this value as read only.
	TypeInfo

	// CanWrite specifies if WriteValue.CanSet() would return true.
	CanWrite bool

	// TopValue is the original value passed to V() but wrapped in a reflect.Value.
	TopValue reflect.Value

	// WriteValue is a reflect.Value representing the modifiable value wrapped within this *Value.
	//
	// If you call V( &t ) then CanWrite will be true and WriteValue will be a usable reflect.Value.
	// If you call V( t ) where t is not a pointer or does not point to allocated memory then
	// CanWrite will be false and attempt to set values on WriteValue will probably panic.
	//
	// All methods on this type that alter the value Append(), Fill*(), To(), etc work on this
	// value.  Generally you should avoid it but it's also present if you really know what you're doing.
	WriteValue reflect.Value

	// When IsMap or IsSlice are true then ElemTypeInfo is a TypeInfo struct describing the element types.
	ElemTypeInfo TypeInfo

	original interface{}
	//
	// We switch out method implementations depending on the original type arg.  We can organize this better
	// but this is a rough first pass for improved benchmarking.
	methodAppend      func(items ...interface{}) error
	methodFields      func() []Field
	methodFieldsByTag func(key string) []Field
	methodNewElem     func() (*Value, error)
	methodZero        func() error
}

// Append appends the item(s) to the end of the Value assuming it is some type of slice and every
// item can be type-coerced into the slice's data type.  Either all items are appended without an error
// or no items are appended and an error is returned describing the type of the item that could not
// be appended.
func (me *Value) Append(items ...interface{}) error {
	if me == nil {
		return errors.NilReceiver()
	}
	return me.methodAppend(items...)
}

// errorUnsupported returns a string that can be used in an error message to indicate the underlying original type
// does not support the requested operation.
func (me *Value) errorUnsupported(method string) string {
	return fmt.Sprintf("%v is unsupported for original type [%T]", method, me.original)
}

func (me *Value) appendSupported(items ...interface{}) error {
	var err error
	func() {
		defer func() {
			if r := recover(); r != nil {
				err = errors.Errorf("%v", r)
			}
		}()
		zero := reflect.Zero(me.Type)
		for _, item := range items {
			elem := reflect.New(me.ElemType)
			elemAsValue := V(elem)
			if err = elemAsValue.To(item); err != nil {
				err = errors.Go(err)
				return
			}
			zero = reflect.Append(zero, reflect.Indirect(elemAsValue.TopValue))
		}
		me.WriteValue.Set(reflect.AppendSlice(me.WriteValue, zero))
	}()
	return err
}

func (me *Value) appendUnsupported(items ...interface{}) error {
	return errors.Errorf(me.errorUnsupported("Append"))
}

// Fields returns a slice of Field structs when Value is wrapped around a struct; for all other values
// nil is returned.
func (me *Value) Fields() []Field {
	return me.methodFields()
}

func (me *Value) fieldsSupported() []Field {
	var rv []Field
	if me != nil && me.IsStruct {
		for k, max := 0, me.Type.NumField(); k < max; k++ {
			v, f := me.WriteValue.Field(k), me.Type.Field(k)
			rv = append(rv, Field{Value: V(v), Field: f})
		}
	}
	return rv
}
func (me *Value) fieldsUnsupported() []Field {
	return nil
}

// FieldByIndex returns the nested field corresponding to index.
//
// Key differences between this method and the built-in method on reflect.Value.FieldByIndex() are
// the built-in causes panics while this one will return errors and this method will instantiate nil struct
// members as it traverses.
func (me *Value) FieldByIndex(index []int) (*Value, error) {
	size := len(index)
	if me == nil {
		return nil, errors.NilReceiver()
	} else if !me.CanWrite {
		return nil, errors.Errorf(error_V_NotAssignable)
	} else if size == 0 {
		return nil, errors.Errorf("Zero length index provided to FieldByIndex()")
	}
	v := me.WriteValue
	for k := 0; k < size; k++ {
		n := index[k] // n is the index (or field num) to consider
		if v.Kind() != reflect.Struct {
			return nil, errors.Errorf("FieldByIndex requires type to be a struct; type is %v", v.Type())
		} else if n > v.NumField() {
			return nil, errors.Errorf("Index out of bounds; field is len %v and index is %v", v.NumField(), n)
		}
		v = v.Field(n)
		t, k := v.Type(), v.Kind()
		// Instantiate nil pointer chains.
		if k == reflect.Ptr {
			for k == reflect.Ptr {
				if v.IsNil() && v.CanSet() {
					ptr := reflect.New(t.Elem())
					v.Set(ptr)
					v = ptr
				}
				v = v.Elem()
				t, k = v.Type(), v.Kind()
			}
		}
	}
	return V(v), nil
}

// FieldsByTag is the same as Fields() except only Fields with the given struct-tag are returned and the
// TagValue member of Field will be set to the tag's value.
func (me *Value) FieldsByTag(key string) []Field {
	return me.methodFieldsByTag(key)
}

func (me *Value) fieldsByTagSupported(key string) []Field {
	var rv []Field
	all := me.Fields()
	for _, f := range all {
		if value, ok := f.Field.Tag.Lookup(key); ok {
			f.TagValue = value
			rv = append(rv, f)
		}
	}
	return rv
}

func (me *Value) fieldsByTagUnsupported(key string) []Field {
	return nil
}

// fill is the underlying function that powers Fill() and FillByTag().
//
// getter is the original Getter passed to Fill() or FillByTag().
//
// Fill() and FillByTag() have essentially the same complicated logic except where they get the string/key to pass
// to getter() and how they sub-fill nested structures.  The keyFunc and fillFunc arguments allow them to
// cascade the appropriate logic into this function.
func (me *Value) fill(getter Getter, fields []Field, keyFunc func(Field) string, fillFunc func(*Value, Getter) error) error {
	var err error
	for _, field := range fields {
		getName := keyFunc(field)
		switch got := getter.Get(getName).(type) {

		case Getter:
			// What was returned from the Getter is itself a Getter; therefore we expect field.Value
			// to be either a struct or []struct that we can sub-fill.
			if field.Value.IsStruct {
				if err = fillFunc(field.Value, got); err != nil {
					return errors.Go(err)
				}
			} else if field.Value.IsSlice && field.Value.ElemTypeInfo.IsStruct {
				if err = field.Value.Zero(); err != nil {
					return errors.Go(err)
				}
				elem := V(reflect.New(field.Value.ElemTypeInfo.Type))
				if err = fillFunc(elem, got); err != nil {
					return errors.Go(err)
				}
				field.Value.Append(elem.WriteValue.Interface()) // This can return an error but it _should_be_ impossible.
			} else {
				return errors.Errorf("Getter.Get( %v ) returned a Getter for field %v and field is not fillable.", getName, field.Field.Name)
			}

		case []Getter:
			// What was returned from the Getter is a []Getter; therefore we expect field.Value to
			// be a []struct or struct that we can sub-fill.
			if field.Value.IsSlice && field.Value.ElemTypeInfo.IsStruct {
				// Zero out the existing slice.
				if err = field.Value.Zero(); err != nil {
					return errors.Go(err)
				}
				for _, elemGetter := range got {
					elem := V(reflect.New(field.Value.ElemTypeInfo.Type))
					if err = fillFunc(elem, elemGetter); err != nil {
						return errors.Go(err)
					}
					field.Value.Append(elem.WriteValue.Interface()) // This can return an error but it _should_be impossible.
				}
			} else if field.Value.IsStruct {
				size := len(got)
				if size > 0 {
					if err = fillFunc(field.Value, got[size-1]); err != nil {
						return errors.Go(err)
					}
				}
			} else {
				return errors.Errorf("Getter.Get( %v ) returned a []Getter for field %v and field is not fillable.", getName, field.Field.Name)
			}

		default:
			if err = field.Value.To(got); err != nil {
				return errors.Go(err)
			}
		}
	}
	return nil
}

// Fill iterates a struct's fields and calls Set() on each one by passing the field name to the Getter.
// Fill stops and returns on the first error encountered.
func (me *Value) Fill(getter Getter) error {
	fields := me.Fields()
	keyFunc := func(field Field) string {
		return field.Field.Name
	}
	fillFunc := func(value *Value, getter Getter) error {
		return value.Fill(getter)
	}
	return me.fill(getter, fields, keyFunc, fillFunc)
}

// FillByTag is the same as Fill() except the argument passed to Getter is the value of the struct-tag.
func (me *Value) FillByTag(key string, getter Getter) error {
	fields := me.FieldsByTag(key)
	keyFunc := func(field Field) string {
		return field.TagValue
	}
	fillFunc := func(value *Value, getter Getter) error {
		return value.FillByTag(key, getter)
	}
	return me.fill(getter, fields, keyFunc, fillFunc)
}

// Zero sets the Value to the Zero value of the appropriate type.
func (me *Value) Zero() error {
	if me == nil {
		return errors.NilReceiver()
	}
	return me.methodZero()
}

func (me *Value) zeroSupported() error {
	me.WriteValue.Set(reflect.Zero(me.Type))
	return nil
}

func (me *Value) zeroUnsupported() error {
	return errors.Errorf(me.errorUnsupported("Zero"))
}

// NewElem instantiates and returns a *Value that can be Panics.Append()'ed to this type; only valid
// if Value.Elem is non-nil.
func (me *Value) NewElem() (*Value, error) {
	if me == nil {
		return nil, errors.NilReceiver()
	}
	return me.methodNewElem()
}

func (me *Value) newElemSupported() (*Value, error) {
	return V(reflect.New(me.ElemType)), nil
}
func (me *Value) newElemUnsupported() (*Value, error) {
	return nil, errors.Errorf(me.errorUnsupported("NewElem"))
}

// To attempts to assign the argument into Value; Value is always set to the Zero value for its type before
// any other assignment ensuring if an assignment fails for any reason that any old data is overwritten.
//
// 	set.V(&T).To(S)
//
//	T is scalar, S is scalar, same type
//		-> direct assignment
//	T is pointer, S is pointer, same type and level of indirection
//		-> direct assignment
//
//	If S is a pointer then dereference until final S value and continue...
//
//	T is scalar, S is scalar, different types
//		-> assignment with attempted type coercion
//	T is scalar, S is slice []S
//		-> T is assigned S[ len( S ) - 1 ]; i.e. last element in S if length greater than 0.
//	T is slice []T, S is scalar
//		-> T is set to []T{ S }; i.e. a slice of T with S as the only element.
//	T is slice []T, S is slice []S
//		-> T is set to []T{ S... }; i.e. a new slice with elements from S copied.
//		-> Note: T != S; they are now different slices; changes to T do not affect S and vice versa.
//		-> Note: If the elements themselves are pointers then, for example, T[0] and S[0] point
//			at the same memory and will see changes to whatever is pointed at.
func (me *Value) To(arg interface{}) error {
	var err error
	//
	dataTypeInfo := TypeCache.Stat(arg)
	if me.original == nil {
		return errors.Errorf(me.errorUnsupported("To"))
	} else if err = me.Zero(); err != nil {
		return err
	} else if arg == nil || dataTypeInfo.Kind == reflect.Invalid {
		return nil
	} else if dataTypeInfo.Type.AssignableTo(me.Type) && me.Kind != reflect.Slice {
		// N.B: We checked that me.Kind is not a slice because this package always makes a copy of a slice!
		me.WriteValue.Set(reflect.ValueOf(arg))
		return nil
	}
	//
	// If arg/data represents any type of pointer we want to get to the final value:
	dataValue := reflect.ValueOf(arg)
	for ; dataValue.Kind() == reflect.Ptr; dataValue = reflect.Indirect(dataValue) {
		if dataValue.IsNil() { // If arg is a pointer and eventually nil we're done because we're already zero value.
			return nil
		}
	}
	//
	if me.IsSlice {
		if !dataTypeInfo.IsSlice {
			arg = []interface{}{arg}
		}
		slice := reflect.ValueOf(arg)
		for k, size := 0, slice.Len(); k < size; k++ {
			elem := V(reflect.New(me.ElemType).Interface())
			if err = elem.To(slice.Index(k).Interface()); err != nil {
				me.Zero()
				return err
			}
			me.WriteValue.Set(reflect.Append(me.WriteValue, elem.WriteValue))
		}
	} else if dataTypeInfo.Kind == reflect.Slice {
		// If the incoming type is slice but ours is not then we call set again using the last element in the slice.
		if dataValue.Len() > 0 {
			return me.To(dataValue.Index(dataValue.Len() - 1).Interface())
		}
	} else if err := coerce(me.WriteValue, dataValue); err != nil {
		return errors.Go(err)
	}
	return nil
}

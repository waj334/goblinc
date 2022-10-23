//go:generate protoc -I. --go-cstruct_opt=paths=source_relative  --go-cstruct_out=. .\tests\test.proto

package tests

import (
	"reflect"
	"testing"
)

func TestGenerator(t *testing.T) {
	tstr := Test0{
		Tuint8:      1,
		Tarruint8:   [4]uint8{2, 3, 4, 5},
		Tint8:       6,
		Tarrint8:    [4]uint8{7, 8, 9, 10},
		Tuint32:     11,
		Tarruint32:  [4]uint32{12, 13, 14, 15},
		Tint32:      16,
		Tarrint32:   [4]uint32{17, 18, 19, 20},
		Tuint64:     21,
		Tarruint64:  [4]uint64{22, 23, 24, 25},
		Tint64:      26,
		Tarrint64:   [4]uint64{27, 28, 29, 30},
		Tfloat32:    31.0,
		Tarrfloat32: [4]float32{32.1, 33.2, 34.3, 35.4},
		Tfloat64:    36,
		Tarrfloat64: [4]float64{37.5, 38.6, 39.7, 40.8},
		Tbytes:      [4]byte{41, 42, 43, 44},
	}

	b := tstr.Bytes()

	tstrDeserialized := Test0{}
	tstrDeserialized.FromBytes(b[:])

	if !reflect.DeepEqual(tstr, tstrDeserialized) {
		t.Fail()
	}
}

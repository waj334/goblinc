package main

import (
	"errors"
	"fmt"
	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
	"google.golang.org/protobuf/types/pluginpb"
	"reflect"
)

var (
	types         = new(protoregistry.Types)
	szWord        = 4
	systemOptions map[string]protoreflect.Value

	ErrUnsupportedFieldType = errors.New("unsupported field type")
)

type Descriptor interface {
	Messages() protoreflect.MessageDescriptors
	Extensions() protoreflect.ExtensionDescriptors
}

type OptionsDescriptor interface {
	ProtoReflect() protoreflect.Message
	Reset()
}

func main() {
	protogen.Options{}.Run(func(plugin *protogen.Plugin) error {
		// Register features
		plugin.SupportedFeatures = uint64(pluginpb.CodeGeneratorResponse_FEATURE_PROTO3_OPTIONAL)

		// Register all extensions
		for _, f := range plugin.Files {
			if err := registerExtensions(types, f.Desc); err != nil {
				panic(err)
			}
		}

		// Generate all sources
		for _, f := range plugin.Files {
			if !f.Generate {
				continue
			}

			// Create system option map
			systemOptions = createOptionsMap[*descriptorpb.FileOptions](f.Desc.Options())

			// Override size of word
			if v, ok := systemOptions["SystemOptions.sizeof_word"]; ok {
				switch v.Enum() {
				case 0:
					szWord = 1
				case 1:
					szWord = 2
				case 2:
					szWord = 4
				case 3:
					szWord = 8
				}
			}

			if err := generateFile(plugin, f); err != nil {
				plugin.Error(err)
			}
		}

		return nil
	})
}

func generateFile(plugin *protogen.Plugin, file *protogen.File) (err error) {
	fname := fmt.Sprintf("%v_cstruct.pb.go", file.GeneratedFilenamePrefix)
	output := plugin.NewGeneratedFile(fname, file.GoImportPath)

	output.P("package ", file.GoPackageName)

	// Generate each message
	for _, msg := range file.Messages {
		if err = generateStruct(plugin, output, msg); err != nil {
			return
		}
	}

	return
}

func generateStruct(plugin *protogen.Plugin, output *protogen.GeneratedFile, msg *protogen.Message) (err error) {
	output.P("type ", msg.GoIdent, " struct {")

	for _, field := range msg.Fields {
		var goType string
		if goType, err = fieldGoType(output, field); err != nil {
			return
		}

		output.P(field.GoName, " ", goType)
	}

	output.P("}")
	output.P("")

	// Generate buffer func
	sizeofMsg := 0
	if sizeofMsg, err = messageSize(msg); err != nil {
		return err
	}

	output.P("func (str *", msg.GoIdent, ") Bytes() (output [", sizeofMsg, "]byte) {")

	// Output message fields
	if err = outputMsgFields(msg, output); err != nil {
		return
	}

	output.P("return")
	output.P("}")
	output.P("")
	output.P("func (str *", msg.GoIdent, ") FromBytes(input []byte) bool {")
	// Input message fields
	if err = inputMsgFields(msg, output); err != nil {
		return
	}
	output.P("return true")
	output.P("}")
	output.P("")
	output.P("func (str *", msg.GoIdent, ") CopyTo(dest []byte) {")
	output.P("data := str.Bytes()")
	output.P("copy(dest, data[:])")
	output.P("}")
	output.P("")
	output.P("func (str *", msg.GoIdent, ") Length() int {")
	output.P("return ", sizeofMsg)
	output.P("}")

	return
}

func outputMsgFields(msg *protogen.Message, output *protogen.GeneratedFile) (err error) {
	offset := 0
	for _, msgField := range msg.Fields {
		options := createOptionsMap[*descriptorpb.FieldOptions](msgField.Desc.Options())

		// Calculate the size of this field in bytes
		baseSz, fieldSz, err := fieldSize(msgField)
		if err != nil {
			return err
		}

		length := 0
		if v, ok := options["FieldOptions.length"]; ok {
			length = int(v.Uint())
		}

		// Calculate boundary starting the next word
		boundary := ((offset / szWord) + 1) * szWord

		// Prepend padding?
		pad := false

		if msgField.Desc.Kind() == protoreflect.MessageKind {
			// Get the size of its first field that is not a message
			submsg := msgField.Message
			for len(submsg.Fields) > 0 {
				if submsg.Fields[0].Desc.Kind() != protoreflect.MessageKind {
					if _, fsz, err := fieldSize(submsg.Fields[0]); err != nil {
						return err
					} else if offset+fsz > boundary {
						// Add padding
						pad = true
					}
					break
				} else {
					submsg = submsg.Fields[0].Message
				}
			}
		} else if length > 0 && baseSz == 1 {
			pad = false
		} else if offset > boundary {
			pad = true
		}

		if pad {
			offset += szWord - (offset % szWord)
		}

		fieldName := fmt.Sprintf("str.%s", msgField.GoName)

		if msgField.Desc.Kind() == protoreflect.DoubleKind {
			funcFloat64bits := output.QualifiedGoIdent(protogen.GoIdent{
				GoName:       "Float64bits",
				GoImportPath: "math",
			})

			// Create a field to convert
			fieldName = fmt.Sprintf("%s_bits", msgField.GoName)
			if msgField.Desc.IsList() {
				output.P(fieldName, " := []uint64{")
				for i := 0; i < length; i++ {
					output.P(funcFloat64bits, "(str.", msgField.GoName, "[", i, "]),")
				}
				output.P("}")
			} else {
				output.P(fieldName, " := ", funcFloat64bits, "(str.", msgField.GoName, ")")
			}
		} else if msgField.Desc.Kind() == protoreflect.FloatKind {
			funcFloat32bits := output.QualifiedGoIdent(protogen.GoIdent{
				GoName:       "Float32bits",
				GoImportPath: "math",
			})

			fieldName = fmt.Sprintf("%s_bits", msgField.GoName)
			if msgField.Desc.IsList() {
				output.P(fieldName, " := []uint32{")
				for i := 0; i < length; i++ {
					output.P(funcFloat32bits, "(str.", msgField.GoName, "[", i, "]),")
				}
				output.P("}")
			} else {
				output.P(fieldName, " := ", funcFloat32bits, "(str.", msgField.GoName, ")")
			}
		}

		if msgField.Desc.IsList() || msgField.Desc.Kind() == protoreflect.BytesKind {
			if baseSz > 1 {
				for i := 0; i < length; i++ {
					// Set each byte individually
					for b := 1; b <= baseSz; b++ {
						// TODO: Take endianness into account
						output.P("output[", offset, "] = byte(", fieldName, "[", i, "] >> (8 * ", baseSz-b, "))")
						offset++
					}
				}
			} else {
				output.P("copy(output[", offset, ":], ", fieldName, "[:])")
				offset += length
			}
		} else if msgField.Desc.Kind() == protoreflect.MessageKind {
			output.P(fieldName, ".CopyTo(output[", offset, ":", offset+fieldSz, "])")
			offset += fieldSz
			continue
		} else {
			if baseSz > 1 {
				// Set each byte individually
				for b := 1; b <= baseSz; b++ {
					// TODO: Take endianness into account
					output.P("output[", offset, "] = byte(", fieldName, " >> (8 * ", baseSz-b, "))")
					offset++
				}
			} else {
				output.P("output[", offset, "] = byte(", fieldName, ")")
				offset++
			}
		}
	}

	return nil
}

func inputMsgFields(msg *protogen.Message, output *protogen.GeneratedFile) (err error) {
	offset := 0
	for _, msgField := range msg.Fields {
		options := createOptionsMap[*descriptorpb.FieldOptions](msgField.Desc.Options())

		// Calculate the size of this field in bytes
		baseSz, fieldSz, err := fieldSize(msgField)
		if err != nil {
			return err
		}

		baseType, err := baseFieldGoType(output, msgField)
		if err != nil {
			return err
		}

		length := 0
		if v, ok := options["FieldOptions.length"]; ok {
			length = int(v.Uint())
		}

		// Calculate boundary starting the next word
		boundary := ((offset / szWord) + 1) * szWord

		// Prepend padding?
		pad := false

		if msgField.Desc.Kind() == protoreflect.MessageKind {
			// Get the size of its first field that is not a message
			submsg := msgField.Message
			for len(submsg.Fields) > 0 {
				if submsg.Fields[0].Desc.Kind() != protoreflect.MessageKind {
					if _, fsz, err := fieldSize(submsg.Fields[0]); err != nil {
						return err
					} else if offset+fsz > boundary {
						// Add padding
						pad = true
					}
					break
				} else {
					submsg = submsg.Fields[0].Message
				}
			}
		} else if length > 0 && baseSz == 1 {
			pad = false
		} else if offset > boundary {
			pad = true
		}

		if pad {
			offset += szWord - (offset % szWord)
		}

		fieldName := fmt.Sprintf("str.%s", msgField.GoName)

		if msgField.Desc.Kind() == protoreflect.DoubleKind {
			funcFloat64bits := output.QualifiedGoIdent(protogen.GoIdent{
				GoName:       "Float64bits",
				GoImportPath: "math",
			})

			// Create a field to convert
			fieldName = fmt.Sprintf("%s_bits", msgField.GoName)
			if msgField.Desc.IsList() {
				output.P(fieldName, " := []uint64{")
				for i := 0; i < length; i++ {
					output.P(funcFloat64bits, "(str.", msgField.GoName, "[", i, "]),")
				}
				output.P("}")
			} else {
				output.P(fieldName, " := uint64(0)")
			}
			baseType = "uint64"
		} else if msgField.Desc.Kind() == protoreflect.FloatKind {
			funcFloat32bits := output.QualifiedGoIdent(protogen.GoIdent{
				GoName:       "Float32bits",
				GoImportPath: "math",
			})

			fieldName = fmt.Sprintf("%s_bits", msgField.GoName)
			if msgField.Desc.IsList() {
				output.P(fieldName, " := []uint32{")
				for i := 0; i < length; i++ {
					output.P(funcFloat32bits, "(str.", msgField.GoName, "[", i, "]),")
				}
				output.P("}")
			} else {
				output.P(fieldName, " := uint32(0)")
			}
			baseType = "uint32"
		}

		if msgField.Desc.IsList() || msgField.Desc.Kind() == protoreflect.BytesKind {
			if baseSz > 1 {
				for i := 0; i < length; i++ {
					// Set each byte individually
					for b := 1; b <= baseSz; b++ {
						// TODO: Take endianness into account
						output.P(fieldName, "[", i, "] |= ", baseType, "(input[", offset, "]) << (8 *", baseSz-b, ")")
						offset++
					}
				}
			} else {
				output.P("copy(", fieldName, "[:], ", "input[", offset, ":]", ")")
				offset += length
			}
		} else if msgField.Desc.Kind() == protoreflect.MessageKind {
			output.P(fieldName, ".FromBytes(input[", offset, ":", offset+fieldSz, "])")
			offset += fieldSz
			continue
		} else {
			if baseSz > 1 {
				// Set each byte individually
				for b := 1; b <= baseSz; b++ {
					// TODO: Take endianness into account
					output.P(fieldName, " |= ", baseType, "(input[", offset, "]) << (8 *", baseSz-b, ")")
					offset++
				}
			} else {
				output.P(fieldName, " = ", baseType, "(input[", offset, "])")
				offset++
			}
		}

		if msgField.Desc.Kind() == protoreflect.DoubleKind {
			funcFloat64frombits := output.QualifiedGoIdent(protogen.GoIdent{
				GoName:       "Float64frombits",
				GoImportPath: "math",
			})

			if msgField.Desc.IsList() {
				for i := 0; i < length; i++ {
					output.P(fmt.Sprintf("str.%s", msgField.GoName), "[", i, "] = ", funcFloat64frombits, "(", fieldName, "[", i, "])")
				}
			} else {
				output.P(fmt.Sprintf("str.%s", msgField.GoName), " = ", funcFloat64frombits, "(", fieldName, ")")
			}
		} else if msgField.Desc.Kind() == protoreflect.FloatKind {
			funcFloat32frombits := output.QualifiedGoIdent(protogen.GoIdent{
				GoName:       "Float32frombits",
				GoImportPath: "math",
			})

			if msgField.Desc.IsList() {
				for i := 0; i < length; i++ {
					output.P(fmt.Sprintf("str.%s", msgField.GoName), "[", i, "] = ", funcFloat32frombits, "(", fieldName, "[", i, "])")
				}
			} else {
				output.P(fmt.Sprintf("str.%s", msgField.GoName), " = ", funcFloat32frombits, "(", fieldName, ")")
			}
		}
	}

	return nil
}

func createOptionsMap[T OptionsDescriptor](options protoreflect.ProtoMessage) map[string]protoreflect.Value {
	// Create service option map
	result := make(map[string]protoreflect.Value)

	// Have to marshal and then unmarshal with dynamic resolver so that extensions are known at runtime.
	optionsMsg := options.(T)
	if b, err := proto.Marshal(optionsMsg); err != nil {
		panic(err)
	} else if !reflect.ValueOf(optionsMsg).IsNil() {
		optionsMsg.Reset()
		unmarshaller := proto.UnmarshalOptions{Resolver: types}
		if err := unmarshaller.Unmarshal(b, optionsMsg); err != nil {
			panic(err)
		}

		// Range over extension fields
		optionsMsg.ProtoReflect().Range(func(descriptor protoreflect.FieldDescriptor, value protoreflect.Value) bool {
			// Skip anything that is not an extension
			if !descriptor.IsExtension() {
				return true
			} else if descriptor.Kind() == protoreflect.MessageKind {
				value.Message().Range(func(descriptor protoreflect.FieldDescriptor, value protoreflect.Value) bool {
					key := fmt.Sprintf("%v.%v", descriptor.ContainingMessage().Name(), descriptor.Name())
					result[key] = value
					return true
				})
			} else {
				result[string(descriptor.Name())] = value
			}

			return true
		})
	}

	return result
}

func registerExtensions(types *protoregistry.Types, descriptor Descriptor) error {
	for i := 0; i < descriptor.Messages().Len(); i++ {
		if err := registerExtensions(types, descriptor.Messages().Get(i)); err != nil {
			return err
		}
	}

	for i := 0; i < descriptor.Extensions().Len(); i++ {
		if err := types.RegisterExtension(dynamicpb.NewExtensionType(descriptor.Extensions().Get(i))); err != nil {
			return err
		}
	}

	return nil
}

func fieldGoType(g *protogen.GeneratedFile, field *protogen.Field) (goType string, err error) {
	// Get field options
	options := createOptionsMap[*descriptorpb.FieldOptions](field.Desc.Options())

	if goType, err = baseFieldGoType(g, field); err != nil {
		return
	}

	if field.Desc.IsList() || field.Desc.Kind() == protoreflect.BytesKind {
		if v, ok := options["FieldOptions.length"]; ok && v.Uint() <= 0 {
			return "", ErrUnsupportedFieldType
		} else {
			goType = fmt.Sprintf("[%d]%s", v.Uint(), goType)
		}
	}

	return
}

func baseFieldGoType(g *protogen.GeneratedFile, field *protogen.Field) (goType string, err error) {
	// Get field options
	options := createOptionsMap[*descriptorpb.FieldOptions](field.Desc.Options())

	if field.Desc.IsWeak() {
		return "", ErrUnsupportedFieldType
	}

	switch field.Desc.Kind() {
	case protoreflect.BoolKind:
		goType = "bool"
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
		if v, ok := options["FieldOptions.bits"]; ok {
			switch v.Enum() {
			case 0:
				goType = "int8"
			case 1:
				goType = "int16"
			}
		} else {
			goType = "int32"
		}
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
		if v, ok := options["FieldOptions.bits"]; ok {
			switch v.Enum() {
			case 0:
				goType = "uint8"
			case 1:
				goType = "uint16"
			}
		} else {
			goType = "uint32"
		}
	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		goType = "int64"
	case protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		goType = "uint64"
	case protoreflect.FloatKind:
		goType = "float32"
	case protoreflect.DoubleKind:
		goType = "float64"
	case protoreflect.BytesKind:
		goType = "byte"
	case protoreflect.MessageKind, protoreflect.GroupKind:
		goType = g.QualifiedGoIdent(field.Message.GoIdent)
	default:
		return "", ErrUnsupportedFieldType
	}

	return
}

func fieldSize(field *protogen.Field) (base, sz int, err error) {
	// Get field options
	options := createOptionsMap[*descriptorpb.FieldOptions](field.Desc.Options())
	createFixedArray := field.Desc.IsList()

	if field.Desc.Kind() == protoreflect.BytesKind {
		createFixedArray = true
	}

	if base, err = baseSize(field, options); err != nil {
		return
	}

	sz = base

	if createFixedArray {
		length := 0
		if v, ok := options["FieldOptions.length"]; ok {
			length = int(v.Uint())
		}

		if length <= 0 {
			return 0, 0, ErrUnsupportedFieldType
		} else {
			sz *= length
		}
	}

	return
}

func messageSize(msg *protogen.Message) (sz int, err error) {
	for _, msgField := range msg.Fields {
		options := createOptionsMap[*descriptorpb.FieldOptions](msgField.Desc.Options())

		length := 0
		if v, ok := options["FieldOptions.length"]; ok {
			length = int(v.Uint())
		}

		baseSz, fieldSz, err := fieldSize(msgField)
		if err != nil {
			return 0, err
		}

		// Boundary of next word
		boundary := ((sz / szWord) + 1) * szWord

		// Prepend padding?
		pad := false

		if msgField.Desc.Kind() == protoreflect.MessageKind {
			// Get the size of its first field that is not a message
			submsg := msgField.Message
			for len(submsg.Fields) > 0 {
				if submsg.Fields[0].Desc.Kind() != protoreflect.MessageKind {
					if _, fsz, err := fieldSize(submsg.Fields[0]); err != nil {
						return 0, err
					} else if sz+fsz > boundary {
						// Add padding
						pad = true
					}
					break
				} else {
					submsg = submsg.Fields[0].Message
				}
			}
		} else if length > 0 && baseSz == 1 {
			pad = false
		} else if sz > boundary {
			pad = true
		}

		if pad {
			sz += szWord - (sz % szWord)
		}

		// Add the size of the field
		sz += fieldSz
	}

	// Account for additional padding at the end for alignment
	if sz%szWord != 0 {
		sz += szWord - (sz % szWord)
	}

	return
}

func baseSize(field *protogen.Field, options map[string]protoreflect.Value) (sz int, err error) {
	switch field.Desc.Kind() {
	case protoreflect.BoolKind:
		sz = 1
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind,
		protoreflect.Uint32Kind, protoreflect.Fixed32Kind, protoreflect.FloatKind:
		if v, ok := options["FieldOptions.bits"]; ok {
			switch v.Enum() {
			case 0: // 8BIT
				sz = 1
			case 1: // 16BIT
				sz = 2
			}
		} else {
			sz = 4
		}
	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind,
		protoreflect.Uint64Kind, protoreflect.Fixed64Kind, protoreflect.DoubleKind:
		sz = 8
	case protoreflect.BytesKind:
		sz = 1
	case protoreflect.MessageKind, protoreflect.GroupKind:
		if sz, err = messageSize(field.Message); err != nil {
			return
		}
	default:
		return 0, ErrUnsupportedFieldType
	}

	return
}

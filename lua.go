package mals

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"reflect"
	"sort"
	"strings"

	"github.com/chainreactors/logs"
	"github.com/chainreactors/utils/iutils"
	lua "github.com/yuin/gopher-lua"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	luar "layeh.com/gopher-luar"

	"github.com/chainreactors/mals/libs/gopher-lua-libs/argparse"
	"github.com/chainreactors/mals/libs/gopher-lua-libs/base64"
	"github.com/chainreactors/mals/libs/gopher-lua-libs/cmd"
	"github.com/chainreactors/mals/libs/gopher-lua-libs/db"
	"github.com/chainreactors/mals/libs/gopher-lua-libs/filepath"
	"github.com/chainreactors/mals/libs/gopher-lua-libs/goos"
	"github.com/chainreactors/mals/libs/gopher-lua-libs/humanize"
	"github.com/chainreactors/mals/libs/gopher-lua-libs/inspect"
	"github.com/chainreactors/mals/libs/gopher-lua-libs/ioutil"
	"github.com/chainreactors/mals/libs/gopher-lua-libs/json"
	"github.com/chainreactors/mals/libs/gopher-lua-libs/log"
	"github.com/chainreactors/mals/libs/gopher-lua-libs/regexp"
	"github.com/chainreactors/mals/libs/gopher-lua-libs/shellescape"
	"github.com/chainreactors/mals/libs/gopher-lua-libs/stats"
	"github.com/chainreactors/mals/libs/gopher-lua-libs/storage"
	luastring "github.com/chainreactors/mals/libs/gopher-lua-libs/strings"
	"github.com/chainreactors/mals/libs/gopher-lua-libs/tcp"
	"github.com/chainreactors/mals/libs/gopher-lua-libs/template"
	"github.com/chainreactors/mals/libs/gopher-lua-libs/time"
	"github.com/chainreactors/mals/libs/gopher-lua-libs/yaml"
	"github.com/cjoudrey/gluahttp"
	gluacrypto_crypto "github.com/tengattack/gluacrypto/crypto"
)

var (
	luaFunctionCache = map[string]lua.LGFunction{}
)

func WrapFuncForLua(fn *MalFunction) lua.LGFunction {
	if luaFn, ok := luaFunctionCache[fn.String()]; ok {
		return luaFn
	}

	luaFn := func(vm *lua.LState) int {
		var args []interface{}
		top := vm.GetTop()

		// 检查最后一个参数是否为回调函数
		var callback *lua.LFunction
		if top > 0 && fn.HasLuaCallback {
			if vm.Get(top).Type() == lua.LTFunction {
				callback = vm.Get(top).(*lua.LFunction)
				top-- // 去掉回调函数，调整参数数量
			}
		}

		// 将 Lua 参数转换为 Go 参数
		for i := 1; i <= top; i++ {
			args = append(args, ConvertLuaValueToGo(vm.Get(i)))
		}
		args, err := ConvertArgsToExpectedTypes(args, fn.ArgTypes)
		if err != nil {
			vm.Error(lua.LString(fmt.Sprintf("Error: %v", err)), 1)
			return 0
		}
		// 调用 Go 函数
		result, err := fn.Func(args...)
		if err != nil {
			vm.Error(lua.LString(fmt.Sprintf("Error: %v", err)), 1)
			return 0
		}

		// 如果有回调，调用回调函数
		if callback != nil {
			vm.Push(callback)
			vm.Push(ConvertGoValueToLua(vm, result))
			if err := vm.PCall(1, 1, nil); err != nil { // 期待一个返回值
				vm.Error(lua.LString(fmt.Sprintf("Callback Error: %v", err)), 1)
				return 0
			}

			return 1
		} else {
			vm.Push(ConvertGoValueToLua(vm, result))
			return 1
		}
	}
	if !fn.NoCache {
		luaFunctionCache[fn.String()] = luaFn
	}

	return luaFn
}

// Convert the []interface{} and map[string]interface{} to the expected types defined in ArgTypes
func ConvertArgsToExpectedTypes(args []interface{}, argTypes []reflect.Type) ([]interface{}, error) {
	if len(args) != len(argTypes) {
		return nil, fmt.Errorf("argument count mismatch: expected %d, got %d", len(argTypes), len(args))
	}

	convertedArgs := make([]interface{}, len(args))

	for i, arg := range args {
		expectedType := argTypes[i]
		val := reflect.ValueOf(arg)

		// Skip conversion if types are already identical
		if val.Type() == expectedType {
			convertedArgs[i] = arg
			continue
		}

		// Handle string conversion with ToString
		if expectedType.Kind() == reflect.String {
			convertedArgs[i] = iutils.ToString(arg)
			continue
		}

		// Handle slice conversion
		if expectedType.Kind() == reflect.Slice && val.Kind() == reflect.Slice {
			elemType := expectedType.Elem()
			sliceVal := reflect.MakeSlice(expectedType, val.Len(), val.Len())
			for j := 0; j < val.Len(); j++ {
				elem := val.Index(j)
				convertedElem, err := convertValueToExpectedType(elem.Interface(), elemType)
				if err != nil {
					return nil, fmt.Errorf("cannot convert slice element at index %d: %v", j, err)
				}
				sliceVal.Index(j).Set(reflect.ValueOf(convertedElem))
			}
			convertedArgs[i] = sliceVal.Interface()
			continue
		}

		// Handle map conversion
		if expectedType.Kind() == reflect.Map && val.Kind() == reflect.Map {
			keyType := expectedType.Key()
			elemType := expectedType.Elem()
			mapVal := reflect.MakeMap(expectedType)
			for _, key := range val.MapKeys() {
				convertedKey, err := convertValueToExpectedType(key.Interface(), keyType)
				if err != nil {
					return nil, fmt.Errorf("cannot convert map key %v: %v", key, err)
				}
				convertedValue, err := convertValueToExpectedType(val.MapIndex(key).Interface(), elemType)
				if err != nil {
					return nil, fmt.Errorf("cannot convert map value for key %v: %v", key, err)
				}
				mapVal.SetMapIndex(reflect.ValueOf(convertedKey), reflect.ValueOf(convertedValue))
			}
			convertedArgs[i] = mapVal.Interface()
			continue
		}

		// Default conversion using reflect.Convert
		if val.Type().ConvertibleTo(expectedType) {
			convertedArgs[i] = val.Convert(expectedType).Interface()
		} else {
			return nil, fmt.Errorf("cannot convert argument %d to %s", i+1, expectedType)
		}
	}
	return convertedArgs, nil
}

// Helper function to convert individual values to the expected type
func convertValueToExpectedType(value interface{}, expectedType reflect.Type) (interface{}, error) {
	val := reflect.ValueOf(value)

	// Skip conversion if types are already identical
	if val.Type() == expectedType {
		return value, nil
	}

	// Handle string conversion
	if expectedType.Kind() == reflect.String {
		return iutils.ToString(value), nil
	}

	// Handle slice conversion
	if expectedType.Kind() == reflect.Slice && val.Kind() == reflect.Slice {
		elemType := expectedType.Elem()
		sliceVal := reflect.MakeSlice(expectedType, val.Len(), val.Len())
		for j := 0; j < val.Len(); j++ {
			convertedElem, err := convertValueToExpectedType(val.Index(j).Interface(), elemType)
			if err != nil {
				return nil, fmt.Errorf("cannot convert slice element at index %d: %v", j, err)
			}
			sliceVal.Index(j).Set(reflect.ValueOf(convertedElem))
		}
		return sliceVal.Interface(), nil
	}

	// Handle map conversion
	if expectedType.Kind() == reflect.Map && val.Kind() == reflect.Map {
		keyType := expectedType.Key()
		elemType := expectedType.Elem()
		mapVal := reflect.MakeMap(expectedType)
		for _, key := range val.MapKeys() {
			convertedKey, err := convertValueToExpectedType(key.Interface(), keyType)
			if err != nil {
				return nil, fmt.Errorf("cannot convert map key %v: %v", key, err)
			}
			convertedValue, err := convertValueToExpectedType(val.MapIndex(key).Interface(), elemType)
			if err != nil {
				return nil, fmt.Errorf("cannot convert map value for key %v: %v", key, err)
			}
			mapVal.SetMapIndex(reflect.ValueOf(convertedKey), reflect.ValueOf(convertedValue))
		}
		return mapVal.Interface(), nil
	}

	// Default conversion
	if val.Type().ConvertibleTo(expectedType) {
		return val.Convert(expectedType).Interface(), nil
	}

	return nil, fmt.Errorf("cannot convert value to %s", expectedType)
}

func isArray(tbl *lua.LTable) bool {
	length := tbl.Len() // Length of the array part
	count := 0
	isSequential := true
	tbl.ForEach(func(key, val lua.LValue) {
		if k, ok := key.(lua.LNumber); ok {
			index := int(k)
			if index != count+1 {
				isSequential = false
			}
			count++
		} else {
			isSequential = false
		}
	})
	return isSequential && count == length
}

// ConvertLuaTableToGo takes a Lua table and converts it into a Go slice or map
func ConvertLuaTableToGo(tbl *lua.LTable) interface{} {
	// Check if the Lua table is an array (keys are sequential integers starting from 1)
	if isArray(tbl) {
		// Convert to Go slice
		var array []interface{}
		tbl.ForEach(func(key, val lua.LValue) {
			array = append(array, ConvertLuaValueToGo(val))
		})
		return array
	}

	// Otherwise, convert to Go map
	m := make(map[string]interface{})
	tbl.ForEach(func(key, val lua.LValue) {
		m[key.String()] = ConvertLuaValueToGo(val)
	})
	return m
}

func ConvertLuaValueToGo(value lua.LValue) interface{} {
	switch v := value.(type) {
	case lua.LString:
		return string(v)
	case lua.LNumber:
		if v == lua.LNumber(int64(v)) {
			return int64(v)
		}
		return float64(v)
	case lua.LBool:
		return bool(v)
	case *lua.LTable:
		return ConvertLuaTableToGo(v)
	case *lua.LUserData:
		return v.Value
	case *lua.LNilType:
		return nil
	case *lua.LFunction:
		return v
	default:
		return v.String()
	}
}

// 将 Lua 的 lua.LValue 转换为 Go 的 interface{}
func ConvertGoValueToLua(L *lua.LState, value interface{}) lua.LValue {
	switch v := value.(type) {
	case proto.Message:
		// 如果是 proto.Message 类型，将其封装为 LUserData 并设置元表
		table1 := L.GetTypeMetatable("ProtobufMessage")
		val := luar.New(L, v)
		mergeLTable(val.(*lua.LUserData).Metatable.(*lua.LTable), table1.(*lua.LTable))
		return val
	case []string:
		// 如果是 []string 类型，将其转换为 Lua 表
		luaTable := L.NewTable()
		for _, str := range v {
			luaTable.Append(lua.LString(str)) // 将每个 string 添加到表中
		}
		return luaTable
	default:
		return luar.New(L, value)
	}
}

func ConvertGoValueToLuaType(L *lua.LState, t reflect.Type) string {
	switch t.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64, reflect.Float32, reflect.Float64, reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return "number"
	case reflect.Bool:
		return "boolean"
	case reflect.String:
		return "string"
	case reflect.Slice:
		if t.Elem().Kind() == reflect.String {
			return "table<string>"
		}
		return "table"
	case reflect.Ptr:
		if t.Implements(reflect.TypeOf((*proto.Message)(nil)).Elem()) {
			return t.Elem().Name()
		}
		if t.Elem().Kind() == reflect.Struct {
			return "table"
		}
		return ConvertGoValueToLuaType(L, t.Elem()) // 递归处理指针类型
	case reflect.Func:
		return "function"
	default:
		if t.Implements(reflect.TypeOf((*context.Context)(nil)).Elem()) {
			return "context"
		}
		return "any"
	}
}

func ConvertNumericType(value int64, kind reflect.Kind) interface{} {
	switch kind {
	case reflect.Int:
		return int(value)
	case reflect.Int8:
		return int8(value)
	case reflect.Int16:
		return int16(value)
	case reflect.Int32:
		return int32(value)
	case reflect.Int64:
		return int64(value)
	case reflect.Uint:
		return uint(value)
	case reflect.Uint8:
		return uint8(value)
	case reflect.Uint16:
		return uint16(value)
	case reflect.Uint32:
		return uint32(value)
	case reflect.Uint64:
		return uint64(value)
	case reflect.Float32:
		return float32(value)
	case reflect.Float64:
		return value
	default:
		return value // 其他类型，保持不变
	}
}

func GlobalLoader(name, path string, content []byte) func(L *lua.LState) int {
	return func(L *lua.LState) int {
		packageTable := L.GetGlobal("package")
		currentPath := L.GetField(packageTable, "path")
		newPath := fmt.Sprintf("%s;%s/?.lua;%s/?/?.lua;%s/?/?/?.lua", currentPath, path, path, path)
		L.SetField(packageTable, "path", lua.LString(newPath))

		if err := L.DoString(string(content)); err != nil {
			logs.Log.Errorf("error loading Lua global script: %s", err.Error())
		}
		mod := L.Get(-1)
		L.Pop(1)

		if mod.Type() != lua.LTTable {
			logs.Log.Errorf("error loading Lua global script: expected table, got %s", mod.Type().String())
			mod = L.NewTable()
		}
		L.SetField(mod, "_NAME", lua.LString(name))
		L.Push(mod)
		return 1
	}
}

func LoadLib(vm *lua.LState) {
	vm.OpenLibs()

	// https://github.com/chainreactors/mals/libs/gopher-lua-libs
	argparse.Preload(vm)
	base64.Preload(vm)
	filepath.Preload(vm)
	goos.Preload(vm)
	humanize.Preload(vm)
	inspect.Preload(vm)
	ioutil.Preload(vm)
	json.Preload(vm)
	//pprof.Preload(vm)
	regexp.Preload(vm)
	//runtime.Preload(vm)
	shellescape.Preload(vm)
	storage.Preload(vm)
	luastring.Preload(vm)
	tcp.Preload(vm)
	time.Preload(vm)
	stats.Preload(vm)
	yaml.Preload(vm)
	db.Preload(vm)
	template.Preload(vm)
	log.Preload(vm)
	cmd.Preload(vm)

	vm.PreloadModule("http", gluahttp.NewHttpModule(&http.Client{}).Loader)
	vm.PreloadModule("crypto", gluacrypto_crypto.Loader)
}

func PackageLoader(funcs map[string]*MalFunction) func(L *lua.LState) int {
	return func(L *lua.LState) int {
		packageName := L.ToString(1)

		mod := L.NewTable()
		L.SetField(mod, "_NAME", lua.LString(packageName))
		// 查找 InternalFunctions 中属于该包的函数并注册
		for _, fn := range funcs {
			mod.RawSetString(fn.Name, L.NewFunction(WrapFuncForLua(fn)))
		}

		// 如果没有找到函数，则返回空表
		L.Push(mod)
		return 1
	}
}

func NewLuaVM() *lua.LState {
	vm := lua.NewState()
	LoadLib(vm)
	RegisterProtobufMessageType(vm)

	return vm
}

// 注册 Protobuf Message 的类型和方法
func RegisterProtobufMessageType(L *lua.LState) {
	mt := L.NewTypeMetatable("ProtobufMessage")
	L.SetGlobal("ProtobufMessage", mt)

	// 注册 __index 和 __newindex 元方法
	//L.SetField(mt, "__index", L.NewFunction(protoIndex))
	L.SetField(mt, "__newindex", L.NewFunction(protoNewIndex))

	// 注册 __tostring 元方法
	L.SetField(mt, "__tostring", L.NewFunction(protoToString))

	L.SetField(mt, "New", L.NewFunction(protoNew))
}

func GenerateLuaDefinitionFile(L *lua.LState, pkg string, protos []string, fns map[string]*MalFunction) error {
	file, err := os.Create(pkg + ".lua")
	if err != nil {
		return err
	}
	defer file.Close()

	generateProtobufMessageClasses(L, file, protos)

	// 按 package 分组，然后在每个分组内按 funcName 排序
	groupedFunctions := make(map[string][]string)
	for funcName, signature := range fns {
		if pkg == signature.Package {
			var group string
			if signature.Helper != nil && signature.Helper.Group != "" {
				group = signature.Helper.Group
			}
			groupedFunctions[group] = append(groupedFunctions[group], funcName)
		}
	}

	// 排序每个 package 内的函数名
	for _, funcs := range groupedFunctions {
		sort.Strings(funcs)
	}

	// 生成 Lua 定义文件
	for group, funcs := range groupedFunctions {
		fmt.Fprintf(file, "-- Group: %s\n\n", group)
		for _, funcName := range funcs {
			signature := fns[funcName]

			fmt.Fprintf(file, "--- %s\n", funcName)

			// Short, Long, Example 描述
			if signature.Helper != nil {
				if signature.Helper.Short != "" {
					for _, line := range strings.Split(signature.Helper.Short, "\n") {
						fmt.Fprintf(file, "--- %s\n", line)
					}
					fmt.Fprintf(file, "---\n")
				}
				if signature.Helper.Long != "" {
					for _, line := range strings.Split(signature.Helper.Long, "\n") {
						fmt.Fprintf(file, "--- %s\n", line)
					}
					fmt.Fprintf(file, "---\n")
				}
				if signature.Helper.Example != "" {
					fmt.Fprintf(file, "--- @example\n")
					for _, line := range strings.Split(signature.Helper.Example, "\n") {
						fmt.Fprintf(file, "--- %s\n", line)
					}
					fmt.Fprintf(file, "---\n")
				}
			}

			// 参数和返回值描述
			var paramsName []string
			for i, argType := range signature.ArgTypes {
				luaType := ConvertGoValueToLuaType(L, argType)
				if signature.Helper != nil && signature.Input != nil {
					keys, values := signature.Helper.FormatInput()
					if len(keys) > 0 {
						paramsName = append(paramsName, keys[i])
						fmt.Fprintf(file, "--- @param %s %s %s\n", keys[i], luaType, values[i])
					}
				} else {
					paramsName = append(paramsName, fmt.Sprintf("arg%d", i+1))
					fmt.Fprintf(file, "--- @param arg%d %s\n", i+1, luaType)
				}
			}
			for _, returnType := range signature.ReturnTypes {
				luaType := ConvertGoValueToLuaType(L, returnType)
				if signature.Helper != nil && signature.Output != nil {
					keys, values := signature.Helper.FormatOutput()
					for i := range keys {
						fmt.Fprintf(file, "--- @return %s %s %s\n", keys[i], luaType, values[i])
					}
				} else {
					fmt.Fprintf(file, "--- @return %s\n", luaType)
				}
			}

			// 函数定义
			fmt.Fprintf(file, "function %s(", funcName)
			for i := range signature.ArgTypes {
				if i > 0 {
					fmt.Fprintf(file, ", ")
				}
				if len(paramsName) > 0 {
					fmt.Fprintf(file, paramsName[i])
				}
			}
			fmt.Fprintf(file, ") end\n\n")
		}
	}

	return nil
}

func GenerateMarkdownDefinitionFile(L *lua.LState, pkg, filename string, fns map[string]*MalFunction) error {
	file, err := os.Create(filename)
	if err != nil {
		return err
	}
	defer file.Close()

	// 按 package 分组，然后在每个分组内按 funcName 排序
	groupedFunctions := make(map[string][]string)
	for funcName, iFunc := range fns {
		if iFunc.Package != pkg {
			continue
		}
		group := "basic"
		if iFunc.Helper != nil && iFunc.Helper.Group != "" {
			group = iFunc.Helper.Group
		}
		groupedFunctions[group] = append(groupedFunctions[group], funcName)
	}
	var groups []string
	for g, _ := range groupedFunctions {
		groups = append(groups, g)
	}

	sort.Strings(groups)
	// 排序每个 package 内的函数名
	for _, funcs := range groupedFunctions {
		sort.Strings(funcs)
	}

	// 生成 Markdown 文档
	for _, pkg := range groups {
		funcs := groupedFunctions[pkg]
		// Package 名称作为二级标题
		fmt.Fprintf(file, "## %s\n\n", pkg)
		for _, funcName := range funcs {
			iFunc := fns[funcName]

			// 函数名作为三级标题
			fmt.Fprintf(file, "### %s\n\n", funcName)

			// 写入 Short 描述
			if iFunc.Helper != nil && iFunc.Helper.Short != "" {
				fmt.Fprintf(file, "%s\n\n", iFunc.Helper.Short)
			}

			// 写入 Long 描述
			if iFunc.Helper != nil && iFunc.Helper.Long != "" {
				for _, line := range strings.Split(iFunc.Helper.Long, "\n") {
					fmt.Fprintf(file, "%s\n", line)
				}
				fmt.Fprintf(file, "\n")
			}

			// 写入参数描述.
			if len(iFunc.ArgTypes) > 0 {
				fmt.Fprintf(file, "**Arguments**\n\n")
				for i, argType := range iFunc.ArgTypes {
					luaType := ConvertGoValueToLuaType(L, argType)
					if iFunc.Helper == nil {
						fmt.Fprintf(file, "- `$%d` [%s] \n", i+1, luaType)
					} else {
						keys, values := iFunc.Helper.FormatInput()
						paramName := fmt.Sprintf("$%d", i+1)
						if i < len(keys) && keys[i] != "" {
							paramName = keys[i]
						}
						description := ""
						if i < len(values) {
							description = values[i]
						}
						fmt.Fprintf(file, "- `%s` [%s] - %s\n", paramName, luaType, description)
					}
				}
			}
			fmt.Fprintf(file, "\n")

			// Example
			if iFunc.Helper != nil && iFunc.Helper.Example != "" {
				fmt.Fprintf(file, "**Example**\n\n```\n")
				for _, line := range strings.Split(iFunc.Helper.Example, "\n") {
					fmt.Fprintf(file, "%s\n", line)
				}
				fmt.Fprintf(file, "```\n\n")
			}
		}
	}

	return nil
}

// generateProtobufMessageClasses 生成 Protobuf message 的 Lua class 定义
func generateProtobufMessageClasses(L *lua.LState, file *os.File, grpcPackage []string) {
	// 使用 protoregistry 遍历所有注册的 Protobuf 结构体
	protoregistry.GlobalTypes.RangeMessages(func(mt protoreflect.MessageType) bool {
		// 获取结构体名称
		messageName := mt.Descriptor().FullName()
		var contains bool
		for _, pkg := range grpcPackage {
			if strings.HasPrefix(string(messageName), pkg) {
				contains = true
			}
		}
		if !contains {
			return true
		}

		// 去掉前缀
		cleanName := removePrefix(string(messageName))

		// 写入 class 定义
		fmt.Fprintf(file, "--- @class %s\n", cleanName)

		fields := mt.Descriptor().Fields()
		for i := 0; i < fields.Len(); i++ {
			field := fields.Get(i)
			luaType := protoFieldToLuaType(field)
			fmt.Fprintf(file, "--- @field %s %s\n", field.Name(), luaType)
		}

		fmt.Fprintf(file, "\n")
		return true
	})
}

// 移除前缀 clientpb 或 implantpb
func removePrefix(messageName string) string {
	i := strings.Index(messageName, ".")
	if i == -1 {
		return messageName
	} else {
		return messageName[i+1:]
	}
}

// protoFieldToLuaType 将 Protobuf 字段映射为 Lua 类型
func protoFieldToLuaType(field protoreflect.FieldDescriptor) string {
	switch field.Kind() {
	case protoreflect.BoolKind:
		return "boolean"
	case protoreflect.Int32Kind, protoreflect.Int64Kind, protoreflect.Uint32Kind, protoreflect.Uint64Kind, protoreflect.FloatKind, protoreflect.DoubleKind:
		return "number"
	case protoreflect.StringKind:
		return "string"
	case protoreflect.BytesKind:
		return "string" // Lua 中处理为 string
	case protoreflect.MessageKind:
		if field.Cardinality() == protoreflect.Repeated {
			return "table"
		}
		return removePrefix(string(field.Message().FullName()))
	case protoreflect.EnumKind:
		return "string" // 枚举可以映射为字符串
	default:
		return "any"
	}
}

// RegisterProtobufMessagesFromPackage 注册指定包中所有的 Protobuf Message
func RegisterProtobufMessagesFromPackage(L *lua.LState, pkg string) {
	// 通过 protoregistry 获取所有注册的消息
	protoregistry.GlobalTypes.RangeMessages(func(mt protoreflect.MessageType) bool {
		messageName := string(mt.Descriptor().FullName())

		// 检查 message 是否属于指定包
		kv := strings.Split(messageName, ".")
		if len(kv) == 2 {
			if pkg == kv[0] {
				RegisterProtobufMessage(L, kv[1], mt.New().Interface().(proto.Message))
			}
		} else {
			return true
		}

		return true
	})
}

// RegisterProtobufMessage 注册 Protobuf message 类型到 Lua
func RegisterProtobufMessage(L *lua.LState, msgType string, msg proto.Message) {
	mt := L.NewTypeMetatable(msgType)
	L.SetGlobal(msgType, mt)

	// 注册 Protobuf 操作
	L.SetField(mt, "__index", L.NewFunction(protoIndex))
	L.SetField(mt, "__newindex", L.NewFunction(protoNewIndex))
	L.SetField(mt, "__tostring", L.NewFunction(protoToString))

	// 新增 New 方法，用于创建该消息的空实例
	L.SetField(mt, "New", L.NewFunction(func(L *lua.LState) int {
		newMsg := proto.Clone(msg).(proto.Message)

		if L.GetTop() == 1 {
			initTable := L.CheckTable(1)
			initTable.ForEach(func(key lua.LValue, value lua.LValue) {
				fieldName := key.String()
				fieldValue := ConvertLuaValueToGo(value)
				setFieldByName(newMsg, fieldName, fieldValue)
			})
		}
		ud := L.NewUserData()
		ud.Value = newMsg
		L.SetMetatable(ud, L.GetTypeMetatable(msgType))

		L.Push(ud)
		return 1 // 返回新建的消息实例
	}))
}

// __tostring 元方法：将 Protobuf 消息转换为字符串
func protoToString(L *lua.LState) int {
	ud := L.CheckUserData(1)
	if msg, ok := ud.Value.(proto.Message); ok {
		// 使用反射遍历并处理 Protobuf 消息的字段
		truncatedMsg := truncateMessageFields(msg)

		// 使用 protojson 将处理后的 Protobuf 消息转换为 JSON 字符串
		marshaler := protojson.MarshalOptions{
			Indent: "  ",
		}
		jsonStr, err := marshaler.Marshal(truncatedMsg)
		if err != nil {
			L.Push(lua.LString(fmt.Sprintf("Error: %v", err)))
		} else {
			L.Push(lua.LString(fmt.Sprintf("<ProtobufMessage: %s> %s", proto.MessageName(msg), string(jsonStr))))
		}
		return 1
	}
	L.Push(lua.LString("<invalid ProtobufMessage>"))
	return 1
}

// truncateLongFields 递归处理 map 中的字符串字段，截断长度超过 1024 的字符串
func truncateMessageFields(msg proto.Message) proto.Message {
	// 创建消息的深拷贝，以避免修改原始消息
	copyMsg := proto.Clone(msg)

	msgValue := reflect.ValueOf(copyMsg).Elem()
	msgType := msgValue.Type()

	for i := 0; i < msgType.NumField(); i++ {
		fieldValue := msgValue.Field(i)

		// 处理字符串类型字段
		if fieldValue.Kind() == reflect.String && fieldValue.Len() > 1024 {
			truncatedStr := fieldValue.String()[:1024] + "......"
			fieldValue.SetString(truncatedStr)
		}

		// 处理字节数组（[]byte）类型字段
		if fieldValue.Kind() == reflect.Slice && fieldValue.Type().Elem().Kind() == reflect.Uint8 {
			// 如果字节数组长度大于 1024，则截断
			if fieldValue.Len() > 1024 {
				truncatedBytes := append(fieldValue.Slice(0, 1024).Bytes(), []byte("......")...)
				fieldValue.SetBytes(truncatedBytes)
			}
		}

		// 处理嵌套的消息类型字段
		if fieldValue.Kind() == reflect.Ptr && !fieldValue.IsNil() && fieldValue.Elem().Kind() == reflect.Struct {
			nestedMsg, ok := fieldValue.Interface().(proto.Message)
			if ok {
				truncateMessageFields(nestedMsg)
			}
		}

		// 处理 repeated 字段（slice 类型）
		if fieldValue.Kind() == reflect.Slice && fieldValue.Type().Elem().Kind() == reflect.Ptr {
			for j := 0; j < fieldValue.Len(); j++ {
				item := fieldValue.Index(j)
				if item.Kind() == reflect.Ptr && item.Elem().Kind() == reflect.Struct {
					nestedMsg, ok := item.Interface().(proto.Message)
					if ok {
						truncateMessageFields(nestedMsg)
					}
				}
			}
		}
	}

	return copyMsg
}

func protoNew(L *lua.LState) int {
	msgTypeName := L.CheckString(1) // 这里确保第一个参数是字符串类型

	// 查找消息类型
	msgType, err := protoregistry.GlobalTypes.FindMessageByName(protoreflect.FullName(msgTypeName))
	if err != nil {
		L.Push(lua.LNil)
		L.Push(lua.LString("invalid message type: " + msgTypeName))
		return 2
	}

	msg := msgType.New().Interface()

	if L.GetTop() > 1 {
		initTable := L.CheckTable(2)
		initTable.ForEach(func(key lua.LValue, value lua.LValue) {
			fieldName := key.String()
			fieldValue := ConvertLuaValueToGo(value)
			setFieldByName(msg, fieldName, fieldValue)
		})
	}
	// 将消息实例返回给 Lua
	ud := L.NewUserData()
	ud.Value = msg
	L.SetMetatable(ud, L.GetTypeMetatable("ProtobufMessage"))
	L.Push(ud)
	return 1
}

// __index 元方法：获取 Protobuf 消息的字段值
func protoIndex(L *lua.LState) int {
	ud := L.CheckUserData(1)
	fieldName := L.CheckString(2)

	if msg, ok := ud.Value.(proto.Message); ok {
		val := getFieldByName(msg, fieldName)
		L.Push(ConvertGoValueToLua(L, val))
		return 1
	}
	return 0
}

// __newindex 元方法：设置 Protobuf 消息的字段值
func protoNewIndex(L *lua.LState) int {
	ud := L.CheckUserData(1)
	fieldName := L.CheckString(2)
	newValue := ConvertLuaValueToGo(L.Get(3))

	if msg, ok := ud.Value.(proto.Message); ok {
		setFieldByName(msg, fieldName, newValue)
	}
	return 0
}

// 使用反射获取字段值
func getFieldByName(msg proto.Message, fieldName string) interface{} {
	val := reflect.ValueOf(msg).Elem().FieldByName(fieldName)
	if val.IsValid() {
		return val.Interface()
	}
	return nil
}

// 使用反射设置字段值
func setFieldByName(msg proto.Message, fieldName string, newValue interface{}) {
	val := reflect.ValueOf(msg).Elem().FieldByName(fieldName)
	if val.IsValid() && val.CanSet() {
		// 将 Lua 值转换为 Go 值并直接设置
		newVal := reflect.ValueOf(newValue)
		// 特别处理 []byte 类型
		if val.Kind() == reflect.Slice && val.Type().Elem().Kind() == reflect.Uint8 {
			if str, ok := newValue.(string); ok {
				newVal = reflect.ValueOf([]byte(str))
			}
		} else if val.Kind() == reflect.Slice && val.Type().Elem().Kind() == reflect.String {
			// 特别处理 []interface{} 到 []string 的转换
			if newVal.Kind() == reflect.Slice && newVal.Type().Elem().Kind() == reflect.Interface {
				slice := newValue.([]interface{})
				strSlice := make([]string, len(slice))
				for i, v := range slice {
					if str, ok := v.(string); ok {
						strSlice[i] = str
					} else {
						fmt.Printf("element %d in %s is not a string\n", i, fieldName)
						return
					}
				}
				newVal = reflect.ValueOf(strSlice)
			}
		}

		// 检查是否可以直接设置值
		if newVal.Type().ConvertibleTo(val.Type()) {
			val.Set(newVal.Convert(val.Type()))
		} else {
			fmt.Printf("Error: cannot convert %s to %s \n", fieldName, val.Type())
		}
	} else {
		fmt.Printf("Error: invalid field: %s \n", fieldName)
	}
}

func mergeLTable(table1, table2 *lua.LTable) {
	table2.ForEach(func(key, value lua.LValue) {
		table1.RawSet(key, value)
	})
}

package mals

import (
	"context"
	"fmt"
	"google.golang.org/protobuf/proto"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
)

type MalFunction struct {
	Name           string
	Package        string
	RawName        string
	Raw            interface{}
	Func           func(...interface{}) (interface{}, error)
	HasLuaCallback bool
	NoCache        bool
	ArgTypes       []reflect.Type
	ReturnTypes    []reflect.Type
	*Helper
}

func (fn *MalFunction) String() string {
	return fmt.Sprintf("%s.%s", fn.Package, fn.Name)
}

// 获取函数的参数和返回值类型
func GetInternalFuncSignature(fn interface{}) *MalFunction {
	fnType := reflect.TypeOf(fn)

	// 获取参数类型
	numArgs := fnType.NumIn()
	argTypes := make([]reflect.Type, numArgs)
	for i := 0; i < numArgs; i++ {
		argTypes[i] = fnType.In(i)
	}

	// 获取返回值类型
	numReturns := fnType.NumOut()
	// 如果最后一个返回值是 error 类型，忽略它
	if numReturns > 0 && fnType.Out(numReturns-1) == reflect.TypeOf((*error)(nil)).Elem() {
		numReturns--
	}
	returnTypes := make([]reflect.Type, numReturns)
	for i := 0; i < numReturns; i++ {
		returnTypes[i] = fnType.Out(i)
	}
	return &MalFunction{
		Raw:         fn,
		RawName:     filepath.Base(runtime.FuncForPC(reflect.ValueOf(fn).Pointer()).Name()),
		ArgTypes:    argTypes,
		ReturnTypes: returnTypes,
	}
}

func WrapInternalFunc(fun interface{}) *MalFunction {
	internalFunc := GetInternalFuncSignature(fun)

	internalFunc.Func = func(params ...interface{}) (interface{}, error) {
		funcValue := reflect.ValueOf(fun)
		funcType := funcValue.Type()

		// 检查函数的参数数量是否匹配
		if funcType.NumIn() != len(params) {
			return nil, fmt.Errorf("expected %d arguments, got %d", funcType.NumIn(), len(params))
		}

		// 构建参数切片并检查参数类型
		in := make([]reflect.Value, len(params))
		for i, param := range params {
			expectedType := funcType.In(i)
			if reflect.TypeOf(param) != expectedType {
				return nil, fmt.Errorf("argument %d should be %v, got %v", i+1, expectedType, reflect.TypeOf(param))
			}
			in[i] = reflect.ValueOf(param)
		}

		// 调用原始函数并获取返回值
		results := funcValue.Call(in)

		// 处理返回值
		var result interface{}
		if len(results) > 0 {
			result = results[0].Interface()
		}

		var err error
		// 如果函数返回了多个值，最后一个值通常是 error
		if len(results) > 1 {
			if e, ok := results[len(results)-1].Interface().(error); ok {
				err = e
			}
		}

		return result, err
	}
	return internalFunc
}

type Helper struct {
	Group   string
	Short   string
	Long    string
	Input   []string
	Output  []string
	Example string
	CMDName string
}

func (help *Helper) FormatInput() ([]string, []string) {
	var keys, values []string
	if help.Input == nil {
		return keys, values
	}

	for _, input := range help.Input {
		i := strings.Index(input, ":")
		if i == -1 {
			keys = append(keys, input)
			values = append(values, "")
		} else {
			keys = append(keys, input[:i])
			values = append(values, input[i+1:])
		}
	}
	return keys, values
}

func (help *Helper) FormatOutput() ([]string, []string) {
	var keys, values []string
	if help.Output == nil {
		return keys, values
	}

	for _, output := range help.Output {
		i := strings.Index(output, ":")
		if i == -1 {
			keys = append(keys, output)
			values = append(values, "")
		} else {
			keys = append(keys, output[:i])
			values = append(values, output[i+1:])
		}
	}
	return keys, values
}

func RegisterGRPCBuiltin(pkg string, rpc interface{}) []*MalFunction {
	rpcType := reflect.TypeOf(rpc)
	rpcValue := reflect.ValueOf(rpc)
	var funcs []*MalFunction
	for i := 0; i < rpcType.NumMethod(); i++ {
		method := rpcType.Method(i)
		methodName := method.Name

		// 忽略流式方法
		methodReturnType := method.Type.Out(0)
		if methodReturnType.Kind() == reflect.Interface && methodReturnType.Name() == "ClientStream" {
			continue
		}

		// 将方法包装为 InternalFunc
		rpcFunc := func(args ...interface{}) (interface{}, error) {
			if len(args) != 2 {
				return nil, fmt.Errorf("expected 2 arguments: context and proto.Message")
			}

			ctx, ok := args[0].(context.Context)
			if !ok {
				return nil, fmt.Errorf("first argument must be context.Context")
			}

			msg, ok := args[1].(proto.Message)
			if !ok {
				return nil, fmt.Errorf("second argument must be proto.Message")
			}

			// 准备调用方法的参数列表
			callArgs := []reflect.Value{
				reflect.ValueOf(ctx), // context.Context
				reflect.ValueOf(msg), // proto.Message
			}

			// 调用方法
			results := rpcValue.MethodByName(methodName).Call(callArgs)

			// 处理返回值
			var result interface{}
			if len(results) > 0 {
				result = results[0].Interface()
			}

			var err error
			if len(results) > 1 {
				if e, ok := results[1].Interface().(error); ok {
					err = e
				}
			}

			return result, err
		}

		// 创建 InternalFunc 实例并设置真实的参数和返回值类型
		internalFunc := GetInternalFuncSignature(method.Func.Interface())
		internalFunc.Func = rpcFunc
		internalFunc.ArgTypes = internalFunc.ArgTypes[1:3]
		internalFunc.Package = pkg
		internalFunc.Name = methodName
		funcs = append(funcs, internalFunc)
	}
	return funcs
}

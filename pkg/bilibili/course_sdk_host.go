package bilibili

import (
	"context"
	"crypto/rand"
	"fmt"
	"math"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

// The pinned official SDK uses a small Emscripten host. Only time, random data
// and guest-memory operations are available; file paths, environment variables
// and stdout/stderr are not exposed to the guest.
func instantiateCourseHost(ctx context.Context, runtime wazero.Runtime, compiled wazero.CompiledModule) error {
	host := runtime.NewHostModuleBuilder("a")
	started := time.Now()
	for _, definition := range compiled.ImportedFunctions() {
		module, name, ok := definition.Import()
		if !ok || module != "a" || len(name) != 1 || name[0] < 'a' || name[0] > 'u' {
			return fmt.Errorf("unsupported classroom SDK import")
		}
		hasResult := len(definition.ResultTypes()) > 0
		host.NewFunctionBuilder().WithGoModuleFunction(api.GoModuleFunc(func(_ context.Context, module api.Module, stack []uint64) {
			memory := module.Memory()
			result := uint32(0)
			switch name {
			case "b": // Date.now
				stack[0] = math.Float64bits(float64(time.Now().UnixMilli()))
				return
			case "h": // performance.now
				stack[0] = math.Float64bits(float64(time.Since(started).Nanoseconds()) / 1e6)
				return
			case "f": // openat: virtual random device only
				path, ok := courseGuestString(memory, uint32(stack[1]))
				if ok && (path == "/dev/urandom" || path == "/dev/random") {
					result = 10
				} else {
					result = 0xffffffd4 // -ENOENT
				}
			case "g": // fd_read
				if stack[0] != 10 {
					result = 8 // EBADF
					break
				}
				total := courseGuestIOVectors(memory, stack, func(data []byte) {
					if _, err := rand.Read(data); err != nil {
						panic("classroom SDK random data unavailable")
					}
				})
				if !memory.WriteUint32Le(uint32(stack[3]), total) {
					panic("classroom SDK invalid read result")
				}
			case "c": // fd_write: discard guest output
				total := courseGuestIOVectors(memory, stack, func([]byte) {})
				if !memory.WriteUint32Le(uint32(stack[3]), total) {
					panic("classroom SDK invalid write result")
				}
			case "k": // memcpy (Go copy also handles overlapping guest ranges)
				data, ok := memory.Read(uint32(stack[1]), uint32(stack[2]))
				if !ok || !memory.Write(uint32(stack[0]), data) {
					panic("classroom SDK invalid memory copy")
				}
			case "p": // environ_sizes_get: no host environment
				if !memory.WriteUint32Le(uint32(stack[0]), 0) || !memory.WriteUint32Le(uint32(stack[1]), 0) {
					panic("classroom SDK invalid environment size")
				}
			case "i": // single-thread runtime
				result = 1
			case "t": // fstat for the virtual random character device
				if stack[0] != 10 {
					result = 0xffffffd4
					break
				}
				data, ok := memory.Read(uint32(stack[1]), 88)
				if !ok {
					panic("classroom SDK invalid device stat")
				}
				clear(data)
				memory.WriteUint32Le(uint32(stack[1])+12, 0x2000)
			case "d", "e", "j", "m", "q", "r", "s": // other filesystem operations
				result = 0xffffffd4
			case "u", "l": // abort / out of memory
				panic("classroom SDK aborted")
			case "a", "n", "o": // close / msync / empty environ_get
			}
			if hasResult {
				stack[0] = uint64(result)
			}
		}), definition.ParamTypes(), definition.ResultTypes()).Export(name)
	}
	_, err := host.Instantiate(ctx)
	return err
}

func courseGuestString(memory api.Memory, address uint32) (string, bool) {
	var path []byte
	for offset := uint32(0); offset < 256; offset++ {
		if uint64(address)+uint64(offset) > math.MaxUint32 {
			return "", false
		}
		value, ok := memory.ReadByte(address + offset)
		if !ok {
			return "", false
		}
		if value == 0 {
			return string(path), true
		}
		path = append(path, value)
	}
	return "", false
}

func courseGuestIOVectors(memory api.Memory, stack []uint64, consume func([]byte)) uint32 {
	if stack[2] > 1024 {
		panic("classroom SDK too many IO vectors")
	}
	var total uint32
	for index := uint64(0); index < stack[2]; index++ {
		address := stack[1] + index*8
		if address+4 > math.MaxUint32 {
			panic("classroom SDK invalid IO vector")
		}
		pointer, pointerOK := memory.ReadUint32Le(uint32(address))
		length, lengthOK := memory.ReadUint32Le(uint32(address + 4))
		if !pointerOK || !lengthOK || uint64(total)+uint64(length) > 1024*1024 {
			panic("classroom SDK invalid IO range")
		}
		data, ok := memory.Read(pointer, length)
		if !ok {
			panic("classroom SDK invalid IO buffer")
		}
		consume(data)
		total += length
	}
	return total
}

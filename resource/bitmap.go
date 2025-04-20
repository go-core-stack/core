// Copyright © 2025 Prabhjot Singh Sethi, All Rights reserved
// Author: Prabhjot Singh Sethi <prabhjot.sethi@gmail.com>

// Initial reference and motivation taken from
// https://github.com/cilium/ipam/blob/master/service/allocator/bitmap.go
// However, we will require to use this while being backed by data store
// thus avoiding usage of big int and replacing it with multi-dimensional
// metrix of int64

package resource

type Bitmap struct {
	//Bits []uint64 `bson:bits,omitempty`
}

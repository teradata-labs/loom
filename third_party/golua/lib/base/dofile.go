package base

import (
	rt "github.com/teradata-labs/loom/third_party/golua/runtime"
)

func dofile(t *rt.Thread, c *rt.GoCont) (rt.Cont, error) {
	chunk, chunkName, err := loadChunk(t, c.Args())
	defer t.ReleaseBytes(len(chunk))
	if err != nil {
		return nil, err
	}
	clos, err := t.LoadFromSourceOrCode(chunkName, chunk, "bt", rt.TableValue(t.GlobalEnv()), true)
	if err != nil {
		return nil, err
	}
	return clos.Continuation(t, c.Next()), nil
}

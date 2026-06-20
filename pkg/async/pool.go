package async

import (
	"fmt"

	"github.com/panjf2000/ants/v2"
)

type Pool struct {
	inner *ants.Pool
}

func NewPool(size int) (*Pool, error) {
	if size <= 0 {
		return nil, fmt.Errorf("async pool size 必须大于 0: %d", size)
	}
	pool, err := ants.NewPool(size)
	if err != nil {
		return nil, fmt.Errorf("创建 async pool 失败: %w", err)
	}
	return &Pool{inner: pool}, nil
}

func (p *Pool) Submit(task func()) error {
	if p == nil || p.inner == nil {
		return fmt.Errorf("async pool 未初始化")
	}
	if task == nil {
		return fmt.Errorf("async pool task 不能为空")
	}
	if err := p.inner.Submit(task); err != nil {
		return fmt.Errorf("提交 async pool task 失败: %w", err)
	}
	return nil
}

func (p *Pool) Tune(size int) error {
	if p == nil || p.inner == nil {
		return fmt.Errorf("async pool 未初始化")
	}
	if size <= 0 {
		return fmt.Errorf("async pool size 必须大于 0: %d", size)
	}
	p.inner.Tune(size)
	return nil
}

func (p *Pool) Cap() int {
	if p == nil || p.inner == nil {
		return 0
	}
	return p.inner.Cap()
}

func (p *Pool) Running() int {
	if p == nil || p.inner == nil {
		return 0
	}
	return p.inner.Running()
}

func (p *Pool) Waiting() int {
	if p == nil || p.inner == nil {
		return 0
	}
	return p.inner.Waiting()
}

func (p *Pool) Free() int {
	if p == nil || p.inner == nil {
		return 0
	}
	return p.inner.Free()
}

func (p *Pool) Release() {
	if p == nil || p.inner == nil {
		return
	}
	p.inner.Release()
}

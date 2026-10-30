package blockcache

import "container/list"

type diskNode struct {
	path      string
	access    uint64
	size      int64
	protected bool
	elem      *list.Element
}
type diskPolicy struct {
	recent, frequent, ghost list.List
	nodes                   map[string]*diskNode
	ghosts                  map[string]*list.Element
	recentBytes             int64
	limit                   int64
}

func newDiskPolicy(limit int64) *diskPolicy {
	return &diskPolicy{nodes: map[string]*diskNode{}, ghosts: map[string]*list.Element{}, limit: limit}
}
func (p *diskPolicy) add(path string, size int64) {
	if p.nodes[path] != nil {
		return
	}
	n := &diskNode{path: path, size: size}
	if g := p.ghosts[path]; g != nil {
		p.ghost.Remove(g)
		delete(p.ghosts, path)
		n.protected = true
		n.elem = p.frequent.PushFront(n)
	} else {
		n.elem = p.recent.PushFront(n)
		p.recentBytes += size
	}
	p.nodes[path] = n
}
func (p *diskPolicy) touch(path string, access uint64) {
	n := p.nodes[path]
	if n == nil {
		return
	}
	if n.protected {
		p.frequent.MoveToFront(n.elem)
	} else if n.access != 0 && n.access != access {
		p.recent.Remove(n.elem)
		p.recentBytes -= n.size
		n.protected = true
		n.elem = p.frequent.PushFront(n)
	}
	n.access = access
}
func (p *diskPolicy) victim() string {
	if p.recentBytes < max(1, p.limit/4) {
		if n := p.frequent.Back(); n != nil {
			return n.Value.(*diskNode).path
		}
	}
	if n := p.recent.Back(); n != nil {
		return n.Value.(*diskNode).path
	}
	if n := p.frequent.Back(); n != nil {
		return n.Value.(*diskNode).path
	}
	return ""
}
func (p *diskPolicy) remove(path string) {
	n := p.nodes[path]
	if n == nil {
		return
	}
	delete(p.nodes, path)
	if n.protected {
		p.frequent.Remove(n.elem)
	} else {
		p.recent.Remove(n.elem)
		p.recentBytes -= n.size
		if old := p.ghosts[path]; old != nil {
			p.ghost.Remove(old)
		}
		p.ghosts[path] = p.ghost.PushFront(path)
	}
	for p.ghost.Len() > 4096 {
		last := p.ghost.Back()
		delete(p.ghosts, last.Value.(string))
		p.ghost.Remove(last)
	}
}

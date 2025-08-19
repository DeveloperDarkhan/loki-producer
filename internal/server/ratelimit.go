package server

import (
	"sync"

	"golang.org/x/time/rate"
)

type perTenantLimiter struct {
	mu       sync.Mutex
	limiters map[string]*rate.Limiter
	rps      float64
	burst    int
}

func newPerTenantLimiter(rps float64, burst int) *perTenantLimiter {
	return &perTenantLimiter{
		limiters: make(map[string]*rate.Limiter),
		rps:      rps,
		burst:    burst,
	}
}

func (p *perTenantLimiter) get(tenant string) *rate.Limiter {
	p.mu.Lock()
	defer p.mu.Unlock()
	lim, ok := p.limiters[tenant]
	if !ok {
		lim = rate.NewLimiter(rate.Limit(p.rps), p.burst)
		p.limiters[tenant] = lim
	}
	return lim
}
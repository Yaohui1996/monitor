package collector

import (
	"sort"
	"strings"

	gnet "github.com/shirou/gopsutil/v4/net"
)

const maxInterfaces = 6

func (c *Collector) readNetwork(dt float64) Network {
	stats, err := gnet.IOCounters(true)
	if err != nil || len(stats) == 0 {
		return c.last.network
	}

	prev := c.prev.net
	if prev == nil {
		prev = make(map[string]netCounters)
	}
	current := make(map[string]netCounters, len(stats))
	interfaces := make([]Interface, 0, len(stats))
	var rxTotal, txTotal uint64

	for _, s := range stats {
		if isLoopback(s.Name) {
			continue
		}
		current[s.Name] = netCounters{rx: s.BytesRecv, tx: s.BytesSent}
		rxTotal += s.BytesRecv
		txTotal += s.BytesSent
		if s.BytesRecv == 0 && s.BytesSent == 0 {
			continue
		}

		var rxRate, txRate float64
		if p, ok := prev[s.Name]; ok && dt > 0 {
			rxRate = rate(s.BytesRecv, p.rx, dt)
			txRate = rate(s.BytesSent, p.tx, dt)
		}
		interfaces = append(interfaces, Interface{
			Name:          s.Name,
			RxBytesPerSec: round(rxRate, 0),
			TxBytesPerSec: round(txRate, 0),
			RxBytesTotal:  s.BytesRecv,
			TxBytesTotal:  s.BytesSent,
		})
	}

	sort.Slice(interfaces, func(i, j int) bool {
		return interfaces[i].RxBytesTotal+interfaces[i].TxBytesTotal >
			interfaces[j].RxBytesTotal+interfaces[j].TxBytesTotal
	})
	if len(interfaces) > maxInterfaces {
		interfaces = interfaces[:maxInterfaces]
	}

	out := Network{
		RxBytesTotal: rxTotal,
		TxBytesTotal: txTotal,
		Interfaces:   interfaces,
	}
	if c.prev.hasNet && dt > 0 {
		out.RxBytesPerSec = round(rate(rxTotal, c.prev.netRx, dt), 0)
		out.TxBytesPerSec = round(rate(txTotal, c.prev.netTx, dt), 0)
	}
	c.prev.net = current
	c.prev.netRx = rxTotal
	c.prev.netTx = txTotal
	c.prev.hasNet = true
	c.last.network = out
	return out
}

func isLoopback(name string) bool {
	n := strings.ToLower(name)
	return n == "lo" || strings.HasPrefix(n, "lo0") || strings.Contains(n, "loopback")
}

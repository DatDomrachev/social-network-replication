package main

import (
	"crypto/rand"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

func uuid4() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

func main() {
	runID := uuid4() // <— метка прогона
	start := time.Now()
	var wg sync.WaitGroup
	success := 0
	mu := sync.Mutex{}

	for i := 0; i < 10000; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			body := strings.NewReader(fmt.Sprintf(`{"data":"RUN:%s; i=%d; ts=%v"}`, runID, i, time.Now()))
			resp, err := http.Post("http://localhost:8080/log/insert", "application/json", body)
			if err == nil && resp.StatusCode == http.StatusOK {
				mu.Lock()
				success++
				mu.Unlock()
			}
		}(i)
		time.Sleep(1 * time.Millisecond) // лёгкий ratecontrol, чтобы успеть «убить» слейв
	}
	wg.Wait()
	fmt.Printf("RUN_ID=%s success=%d/10000 duration=%v\n", runID, success, time.Since(start))
}

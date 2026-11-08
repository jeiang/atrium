package assistant

import "time"

func Now() float64 { return float64(time.Now().UnixMicro()) / 1e6 }

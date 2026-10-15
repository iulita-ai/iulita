package runtime

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// The supplied public ingress deliberately excludes this operational endpoint.
// Restrict the service port to trusted ingress/monitoring peers at deployment.
func (r *Runtime) metrics(w http.ResponseWriter, request *http.Request) {
	ctx, cancel := context.WithTimeout(request.Context(), 2*time.Second)
	defer cancel()
	d := r.grpc.Diagnostics()
	var body strings.Builder
	fmt.Fprintf(&body, "# TYPE iulita_mobile_rpc_active gauge\niulita_mobile_rpc_active %d\n# TYPE iulita_mobile_rpc_completed_total counter\n", d.Active)
	for i, n := range d.Codes {
		fmt.Fprintf(&body, "iulita_mobile_rpc_completed_total{code=\"%d\"} %d\n", i, n)
	}
	body.WriteString("# TYPE iulita_mobile_rpc_success_latency_bucket_total counter\n")
	for i, n := range d.SuccessLatencyBuckets {
		fmt.Fprintf(&body, "iulita_mobile_rpc_success_latency_bucket_total{bucket=\"%d\"} %d\n", i, n)
	}
	if r.devices != nil {
		phones, err := r.devices.Diagnostics(ctx)
		if err != nil {
			w.WriteHeader(503)
			return
		}
		body.WriteString("# TYPE iulita_mobile_phone_retained_commands gauge\n")
		// Ordinal labels are a fixed server enum, never input from a device.
		for i, n := range phones.Commands {
			fmt.Fprintf(&body, "iulita_mobile_phone_retained_commands{state=\"%d\"} %d\n", i, n)
		}
		fmt.Fprintf(&body, "# TYPE iulita_mobile_phone_retained_deliveries gauge\niulita_mobile_phone_retained_deliveries %d\n# TYPE iulita_mobile_phone_delivery_seconds_sum gauge\niulita_mobile_phone_delivery_seconds_sum %g\n", phones.Delivered, phones.DeliverySeconds)
		fmt.Fprintf(&body, "# TYPE iulita_mobile_phone_retained_successes gauge\niulita_mobile_phone_retained_successes %d\n# TYPE iulita_mobile_phone_success_seconds_sum gauge\niulita_mobile_phone_success_seconds_sum %g\n", phones.Succeeded, phones.SuccessSeconds)
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if _, writeErr := w.Write([]byte(body.String())); writeErr != nil {
		return
	}
}

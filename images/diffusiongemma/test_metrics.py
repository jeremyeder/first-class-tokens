import base64
import importlib.util
import http.server
import pathlib
import sys
import threading
import types
import unittest
import urllib.error
import urllib.request


# The image's vLLM base supplies these modules. Stub only the imports needed to
# load the dependency-free metrics helpers in a local source-only test.
sys.modules.setdefault("pybase64", types.SimpleNamespace(b64encode=base64.b64encode))
transformers = types.ModuleType("transformers")
transformers.AutoTokenizer = object
sys.modules.setdefault("transformers", transformers)

source = pathlib.Path(__file__).with_name("structured_server.py")
spec = importlib.util.spec_from_file_location("structured_server", source)
server = importlib.util.module_from_spec(spec)
spec.loader.exec_module(server)


class MetricStoreTest(unittest.TestCase):
    def test_render_is_prometheus_text_and_histogram_buckets_are_cumulative(self):
        store = server.MetricStore()
        requests = store.counter("demo_requests_total", "Requests", ("route", "result"))
        latency = store.histogram("demo_latency_seconds", "Latency", ("route",), (0.1, 1))

        requests.inc(labels=("systemone", "success"))
        requests.inc(labels=("systemone", "success"))
        latency.observe(0.05, labels=("systemone",))
        latency.observe(0.5, labels=("systemone",))

        rendered = store.render()
        self.assertIn('# TYPE demo_requests_total counter', rendered)
        self.assertIn('demo_requests_total{route="systemone",result="success"} 2', rendered)
        self.assertIn('demo_latency_seconds_bucket{route="systemone",le="0.1"} 1', rendered)
        self.assertIn('demo_latency_seconds_bucket{route="systemone",le="1"} 2', rendered)
        self.assertIn('demo_latency_seconds_bucket{route="systemone",le="+Inf"} 2', rendered)
        self.assertIn('demo_latency_seconds_count{route="systemone"} 2', rendered)

    def test_choice_mapping_is_bounded(self):
        self.assertEqual(server._metric_choice("First class"), "first")
        self.assertEqual(server._metric_choice("fare-business"), "business")
        self.assertEqual(server._metric_choice("customer-123"), "other")

    def test_metrics_endpoint_proxies_only_upstream_metrics(self):
        class UpstreamHandler(http.server.BaseHTTPRequestHandler):
            def do_GET(self):
                if self.path == "/metrics":
                    body = b"# TYPE vllm_test gauge\nvllm_test 1\n"
                    self.send_response(200)
                    self.send_header("content-length", str(len(body)))
                    self.end_headers()
                    self.wfile.write(body)
                    return
                self.send_response(404)
                self.end_headers()

            def log_message(self, *_args):
                pass

        upstream = server.ThreadingHTTPServer(("127.0.0.1", 0), UpstreamHandler)
        threading.Thread(target=upstream.serve_forever, daemon=True).start()
        adapter = None
        try:
            server.ARGS = types.SimpleNamespace(
                upstream=f"http://127.0.0.1:{upstream.server_address[1]}"
            )
            adapter = server.ThreadingHTTPServer(("127.0.0.1", 0), server.Handler)
            threading.Thread(target=adapter.serve_forever, daemon=True).start()
            base = f"http://127.0.0.1:{adapter.server_address[1]}"
            metrics = urllib.request.urlopen(base + "/metrics", timeout=2).read().decode()
            self.assertIn("first_class_tokens_model_vllm_up", metrics)
            self.assertIn("first_class_tokens_model_vllm_up 1", metrics)
            self.assertIn("vllm_test 1", metrics)
            try:
                urllib.request.urlopen(base + "/v1/raw/chat/completions", timeout=2)
                self.fail("raw vLLM API unexpectedly exposed through adapter")
            except urllib.error.HTTPError as error:
                self.assertEqual(error.code, 404)
                error.close()
        finally:
            if adapter is not None:
                adapter.shutdown()
                adapter.server_close()
            upstream.shutdown()
            upstream.server_close()


if __name__ == "__main__":
    unittest.main()

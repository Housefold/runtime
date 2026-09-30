import contextlib
import io
import json
import threading
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

import haos_synthetic as driver


class SyntheticDriverTests(unittest.TestCase):
    def arguments(self, *extra):
        return driver.arguments(["burst", "--base-url", self.base,
                                 "--isolated-test", *extra])

    def setUp(self):
        self.requests = []
        self.redirect = False
        owner = self

        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *_):
                pass

            def do_POST(self):
                body = self.rfile.read(int(self.headers.get("Content-Length", "0")))
                owner.requests.append((self.path, self.headers.get("Authorization"), json.loads(body)))
                if owner.redirect:
                    self.send_response(302)
                    self.send_header("Location", owner.base + "/credential_sink")
                else:
                    self.send_response(201)
                self.end_headers()

            def do_DELETE(self):
                owner.requests.append((self.path, self.headers.get("Authorization"), None))
                self.send_response(404)  # cleanup is idempotent
                self.end_headers()

        self.server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.base = f"http://127.0.0.1:{self.server.server_port}"
        self.thread = threading.Thread(target=self.server.serve_forever)
        self.thread.start()

    def tearDown(self):
        self.server.shutdown()
        self.server.server_close()
        self.thread.join()

    def test_bounded_namespace_and_sequence(self):
        args = self.arguments("--count", "2", "--rounds", "3", "--blob-bytes", "7")
        self.assertEqual(driver.run(args, "synthetic-test-token"), 6)
        for n, (path, auth, body) in enumerate(self.requests):
            self.assertEqual(path, f"/api/states/sensor.housefold_test_{n % 2}")
            self.assertEqual(auth, "Bearer synthetic-test-token")
            self.assertEqual(body["state"], str(n // 2))
            self.assertEqual(body["attributes"]["blob"], "x" * 7)
            self.assertTrue(body["attributes"]["synthetic"])

    def test_redirect_never_forwards_test_credential(self):
        self.redirect = True
        with self.assertRaises(driver.urllib.error.HTTPError):
            driver.run(self.arguments("--count", "1"), "synthetic-test-token")
        self.assertEqual(len(self.requests), 1)

    def test_cleanup_is_idempotent(self):
        args = driver.arguments(["cleanup", "--base-url", self.base, "--isolated-test", "--count", "2"])
        self.assertEqual(driver.run(args, "synthetic-test-token"), 2)
        self.assertTrue(all(body is None for _, _, body in self.requests))

    def test_invalid_configuration_sends_nothing(self):
        with contextlib.redirect_stderr(io.StringIO()):
            for extra in [["--count", "257"], ["--count", "256", "--rounds", "4096"]]:
                with self.assertRaises(SystemExit):
                    self.arguments(*extra)
            for url in ["ftp://localhost", "https://user:token@localhost", "https://localhost/path"]:
                with self.assertRaises(SystemExit):
                    driver.arguments(["seed", "--base-url", url, "--isolated-test"])
            with self.assertRaises(SystemExit):
                driver.arguments(["seed", "--base-url", self.base])
        for token in ["", "bad\r\nheader"]:
            with self.assertRaises(ValueError):
                driver.run(self.arguments(), token)
        self.assertEqual(self.requests, [])


if __name__ == "__main__":
    unittest.main()

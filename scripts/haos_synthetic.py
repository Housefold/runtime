#!/usr/bin/env python3
"""Bounded artificial states for a separately authorized, isolated HAOS test.

Never imports household configuration, invokes services or discovers entities.
Only sensor.housefold_test_N is written/deleted. Do not run against a real home.
"""
import argparse
import json
import os
import sys
import time
import urllib.error
import urllib.parse
import urllib.request


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


def bounded(low, high):
    def parse(value):
        value = int(value)
        if not low <= value <= high:
            raise argparse.ArgumentTypeError(f"must be between {low} and {high}")
        return value
    return parse


def arguments(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("mode", choices=["seed", "burst", "cleanup"])
    parser.add_argument("--base-url", required=True, help="explicit isolated HA Core URL")
    parser.add_argument("--isolated-test", required=True, action="store_true",
                        help="assert that this is an authorized disposable synthetic HA instance")
    parser.add_argument("--count", type=bounded(1, 256), default=16)
    parser.add_argument("--rounds", type=bounded(1, 4096), default=1)
    parser.add_argument("--blob-bytes", type=bounded(0, 262144), default=1024)
    parser.add_argument("--interval-ms", type=bounded(0, 60000), default=0)
    args = parser.parse_args(argv)
    url = urllib.parse.urlsplit(args.base_url)
    if (url.scheme not in ("http", "https") or not url.hostname or url.username
            or url.password or url.path not in ("", "/") or url.query or url.fragment):
        parser.error("base URL must be an HTTP(S) origin without credentials/path/query")
    if args.count * args.rounds > 65536:
        parser.error("at most 65536 synthetic writes per invocation")
    args.base_url = args.base_url.rstrip("/")
    return args


def run(args, token):
    if not token or any(c in token for c in "\r\n"):
        raise ValueError("test credential unavailable")
    opener = urllib.request.build_opener(NoRedirect())
    rounds = args.rounds if args.mode == "burst" else 1
    writes = 0
    for tick in range(rounds):
        for index in range(args.count):
            entity = f"sensor.housefold_test_{index}"
            body = None
            method = "DELETE" if args.mode == "cleanup" else "POST"
            if method == "POST":
                body = json.dumps({"state": str(tick), "attributes": {
                    "synthetic": True, "sequence": tick,
                    "blob": "x" * args.blob_bytes}}, separators=(",", ":")).encode()
            request = urllib.request.Request(
                args.base_url + "/api/states/" + entity, data=body, method=method,
                headers={"Authorization": "Bearer " + token, "Content-Type": "application/json"})
            try:
                with opener.open(request, timeout=10) as response:
                    if response.status not in (200, 201):
                        raise ValueError("unexpected test response")
                    # Do not decode/retain response state or print its body.
            except urllib.error.HTTPError as error:
                if method != "DELETE" or error.code != 404:
                    raise
            writes += 1
            if args.interval_ms:
                time.sleep(args.interval_ms / 1000)
    return writes


def main():
    args = arguments()
    try:
        writes = run(args, os.environ.get("HA_TEST_TOKEN", ""))
    except (ValueError, urllib.error.URLError, OSError):
        # Exception text may contain a URL or other sensitive context.
        print("Synthetic test request failed; check test access locally.", file=sys.stderr)
        return 1
    print(f"Completed {writes} synthetic {args.mode} requests.")
    return 0


if __name__ == "__main__":
    sys.exit(main())

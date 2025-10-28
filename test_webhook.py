#!/usr/bin/env python3
from http.server import HTTPServer, BaseHTTPRequestHandler
import json

class WebhookHandler(BaseHTTPRequestHandler):
    def do_POST(self):
        content_length = int(self.headers['Content-Length'])
        post_data = self.rfile.read(content_length)

        print("=" * 80)
        print("Webhook received!")
        print("=" * 80)

        try:
            email_data = json.loads(post_data)
            print(f"From: {email_data.get('from')}")
            print(f"To: {email_data.get('to')}")
            print(f"Subject: {email_data.get('subject')}")
            print(f"Body: {email_data.get('body')}")
            print(f"Timestamp: {email_data.get('timestamp')}")
            print("=" * 80)
        except json.JSONDecodeError:
            print(f"Raw data: {post_data.decode('utf-8')}")

        self.send_response(200)
        self.send_header('Content-type', 'text/plain')
        self.end_headers()
        self.wfile.write(b'OK')

    def log_message(self, format, *args):
        # Suppress default logging
        pass

if __name__ == '__main__':
    server = HTTPServer(('localhost', 8888), WebhookHandler)
    print("Webhook server listening on http://localhost:8888")
    print("Waiting for webhooks...")
    server.serve_forever()

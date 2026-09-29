import http.server

http.server.HTTPServer(("", 8080), http.server.SimpleHTTPRequestHandler).serve_forever()

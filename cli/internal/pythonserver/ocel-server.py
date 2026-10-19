import ast
import importlib
import importlib.util
import inspect
import os
import runpy
import sys

APP_NAMES = ("app", "application")
FORMS = ("file", "module")
UNSCOPED = (
    ast.FunctionDef,
    ast.AsyncFunctionDef,
    ast.ClassDef,
    ast.Lambda,
    ast.ListComp,
    ast.SetComp,
    ast.DictComp,
    ast.GeneratorExp,
)


def fail(message):
    sys.exit("ocel: " + message)


def bound_names(path):
    with open(path, "rb") as source:
        tree = ast.parse(source.read(), path)
    names = set()
    pending = list(tree.body)
    while pending:
        node = pending.pop()
        if isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef, ast.ClassDef)):
            names.add(node.name)
        elif isinstance(node, ast.ImportFrom):
            names.update(alias.asname or alias.name for alias in node.names)
        elif isinstance(node, ast.Import):
            names.update(alias.asname for alias in node.names if alias.asname)
        elif isinstance(node, ast.Name) and isinstance(node.ctx, ast.Store):
            names.add(node.id)
        if not isinstance(node, UNSCOPED):
            pending.extend(ast.iter_child_nodes(node))
    return names


def load_file(name, path):
    spec = importlib.util.spec_from_file_location(name, path)
    module = importlib.util.module_from_spec(spec)
    sys.modules[name] = module
    spec.loader.exec_module(module)
    return module


def is_asgi(app):
    return inspect.iscoroutinefunction(app) or (callable(app) and inspect.iscoroutinefunction(type(app).__call__))


def serve_asgi(entrypoint, name, app, host, port):
    try:
        import uvicorn
    except ImportError:
        fail(
            f"{entrypoint} defines {name}, an ASGI app, and uvicorn is not installed to serve it: add uvicorn to the app's dependencies"
        )
    uvicorn.run(app, host=host, port=port, interface="asgi3")


def serve_wsgi(entrypoint, app, host, port):
    try:
        from gunicorn.app.base import BaseApplication
    except ImportError:
        serve_wsgiref(entrypoint, app, host, port)
        return

    class Server(BaseApplication):
        def load_config(self):
            self.cfg.set("bind", f"{host}:{port}")
            self.cfg.set("worker_class", "gthread")
            self.cfg.set("threads", 8)
            self.cfg.set("timeout", 0)
            if "control_socket_disable" in self.cfg.settings:
                self.cfg.set("control_socket_disable", True)

        def load(self):
            return app

    Server().run()


def serve_wsgiref(entrypoint, app, host, port):
    from socketserver import ThreadingMixIn
    from wsgiref.simple_server import WSGIServer, make_server

    class ThreadingWSGIServer(ThreadingMixIn, WSGIServer):
        daemon_threads = True

    print(
        f"ocel: serving {entrypoint} with the standard library's wsgiref: add gunicorn to the app's dependencies to serve it with a production server",
        file=sys.stderr,
        flush=True,
    )
    make_server(host, port, app, ThreadingWSGIServer).serve_forever()


class Entrypoint:
    def __init__(self, form, declared, root):
        self.declared = declared
        self.is_file = form == "file"
        if self.is_file:
            self.path = os.path.join(root, *declared.split("/"))
            self.module_name = declared[: -len(".py")].replace("/", ".")
            self.named = None
            if os.path.dirname(self.path) != root:
                sys.path.insert(0, os.path.dirname(self.path))
            return
        self.module_name, _, self.named = declared.partition(":")
        try:
            spec = importlib.util.find_spec(self.module_name)
        except ModuleNotFoundError:
            spec = None
        if spec is None or spec.origin is None:
            fail(f"{declared} names module {self.module_name}, and nothing on the path is a module of that name")
        self.path = spec.origin

    def defines_app(self):
        return bool(self.named) or bool(bound_names(self.path) & set(APP_NAMES))

    def run_as_script(self):
        if self.is_file:
            runpy.run_path(self.path, run_name="__main__")
        else:
            runpy.run_module(self.module_name, run_name="__main__", alter_sys=True)

    def load(self):
        if self.is_file:
            return load_file(self.module_name, self.path)
        return importlib.import_module(self.module_name)


def main():
    arguments = sys.argv[1:]
    asks_defines_app = arguments[:1] == ["--defines-app"]
    if asks_defines_app:
        arguments = arguments[1:]
    if len(arguments) != 2 or arguments[0] not in FORMS:
        fail("ocel-server.py takes the form of the entrypoint it serves, file or module, and the entrypoint: a .py path or module[:name]")
    root = os.path.dirname(os.path.abspath(__file__))
    if root not in sys.path:
        sys.path.insert(0, root)
    entrypoint = Entrypoint(arguments[0], arguments[1], root)
    if asks_defines_app:
        print("yes" if entrypoint.defines_app() else "no")
        return
    sys.argv[:] = [entrypoint.path]

    if not entrypoint.defines_app():
        entrypoint.run_as_script()
        fail(
            f"{entrypoint.declared} defines neither app nor application, so it runs as a script, and it returned without serving"
        )

    port = os.environ.get("PORT")
    if not port:
        fail(f"PORT names no port to serve {entrypoint.declared} on")
    host = os.environ.get("HOST") or "127.0.0.1"
    module = entrypoint.load()
    names = (entrypoint.named,) if entrypoint.named else APP_NAMES
    for name in names:
        app = getattr(module, name, None)
        if app is None:
            continue
        if is_asgi(app):
            serve_asgi(entrypoint.declared, name, app, host, int(port))
        else:
            serve_wsgi(entrypoint.declared, app, host, int(port))
        return
    fail(f"{entrypoint.declared} defines no {' or '.join(names)} once imported")


main()

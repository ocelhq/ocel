import ast
import json
import os
import sys

DYNAMIC = {"import_module", "__import__"}
NOT_AN_ENTRY = ("conftest.py",)


class Unresolved(Exception):
    def __init__(self, file, line):
        self.file = file
        self.line = line


class Unparsed(Exception):
    def __init__(self, file, line, message):
        self.file = file
        self.line = line
        self.message = message


def entries(app_dir):
    found = []
    for name in sorted(os.listdir(app_dir)):
        path = os.path.join(app_dir, name)
        if name.endswith(".py") and not run_by_a_test_runner(name) and os.path.isfile(path):
            found.append(path)
    return found


def run_by_a_test_runner(name):
    return (
        name == "__init__.py"
        or name in NOT_AN_ENTRY
        or name.startswith("test_")
        or name.endswith("_test.py")
    )


def module_files(module, search):
    for directory in search:
        base = os.path.join(directory, *module.split("."))
        package = os.path.join(base, "__init__.py")
        if os.path.isfile(package):
            return package
        if os.path.isfile(base + ".py"):
            return base + ".py"
    return None


def packages_above(module):
    parts = module.split(".")
    return [".".join(parts[:i]) for i in range(1, len(parts))]


def package_of(file, level):
    directory = os.path.dirname(file)
    for _ in range(level - 1):
        directory = os.path.dirname(directory)
    return directory


def dynamic(call):
    if isinstance(call.func, ast.Attribute):
        return call.func.attr in DYNAMIC
    if isinstance(call.func, ast.Name):
        return call.func.id in DYNAMIC
    return False


def written_out(call):
    if call.args and isinstance(call.args[0], ast.Constant) and isinstance(call.args[0].value, str):
        return call.args[0].value
    return None


def imported(file, search):
    try:
        with open(file, "rb") as source:
            tree = ast.parse(source.read(), filename=file)
    except SyntaxError as error:
        raise Unparsed(file, error.lineno or 0, error.msg) from None

    reached = []

    def resolve(module, directories):
        for above in packages_above(module) + [module]:
            found = module_files(above, directories)
            if found:
                reached.append(found)

    for node in ast.walk(tree):
        if isinstance(node, ast.Call) and dynamic(node):
            module = written_out(node)
            if module is None:
                raise Unresolved(file, node.lineno)
            resolve(module, search)
        if isinstance(node, ast.Import):
            for alias in node.names:
                resolve(alias.name, search)
        elif isinstance(node, ast.ImportFrom):
            directories = [package_of(file, node.level)] if node.level else search
            base = node.module or ""
            if base:
                resolve(base, directories)
            for alias in node.names:
                child = f"{base}.{alias.name}" if base else alias.name
                found = module_files(child, directories)
                if found:
                    reached.append(found)
    return reached


def closure(entry, search):
    seen = set()
    pending = [entry]
    while pending:
        file = pending.pop()
        if file in seen:
            continue
        seen.add(file)
        pending.extend(imported(file, search))
    return sorted(seen)


def main():
    app_dir = os.path.abspath(sys.argv[1])
    search = [app_dir] + [os.path.abspath(d) for d in sys.argv[2:]]
    reached = {}
    try:
        for entry in entries(app_dir):
            reached[entry] = closure(entry, search)
    except Unresolved as unresolved:
        json.dump({"error": {"file": unresolved.file, "line": unresolved.line}}, sys.stdout)
        return
    except Unparsed as unparsed:
        json.dump(
            {"error": {"file": unparsed.file, "line": unparsed.line, "message": unparsed.message}},
            sys.stdout,
        )
        return
    json.dump({"entries": reached}, sys.stdout)


main()

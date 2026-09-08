import ast
import json
import os
import sys

COMPUTED = {"import_module", "__import__"}


class Unresolved(Exception):
    def __init__(self, file, line):
        self.file = file
        self.line = line


def entries(app_dir):
    found = []
    for name in sorted(os.listdir(app_dir)):
        path = os.path.join(app_dir, name)
        if name.endswith(".py") and name != "__init__.py" and os.path.isfile(path):
            found.append(path)
    return found


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


def computed(call):
    if isinstance(call.func, ast.Attribute):
        name = call.func.attr
    elif isinstance(call.func, ast.Name):
        name = call.func.id
    else:
        return False
    if name not in COMPUTED:
        return False
    return not (call.args and isinstance(call.args[0], ast.Constant) and isinstance(call.args[0].value, str))


def imported(file, search):
    with open(file, "rb") as source:
        tree = ast.parse(source.read(), filename=file)

    reached = []

    def resolve(module, directories):
        for above in packages_above(module) + [module]:
            found = module_files(above, directories)
            if found:
                reached.append(found)

    for node in ast.walk(tree):
        if isinstance(node, ast.Call) and computed(node):
            raise Unresolved(file, node.lineno)
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
    json.dump({"entries": reached}, sys.stdout)


main()

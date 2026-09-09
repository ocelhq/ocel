import ocel

db = ocel.postgres("main")


class Env(ocel.Env):
    greeting: str = "hello"

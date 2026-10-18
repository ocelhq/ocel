from importlib.resources import files


def test_the_package_marks_itself_typed_for_type_checkers():
    assert files("ocel").joinpath("py.typed").is_file()

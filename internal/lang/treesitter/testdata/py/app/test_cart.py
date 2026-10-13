from app.cart import total


def test_total():
    assert total([]) == 0

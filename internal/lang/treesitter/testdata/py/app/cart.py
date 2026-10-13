from app.core.price import price
from .item import Item


def total(items):
    """Sum a cart."""
    s = 0
    for it in items:
        if it.qty > 0 and it.live:
            s += price(it)
    return s


def _unused():
    pass


class Cart:
    def add(self, i: Item) -> None:
        total([i])

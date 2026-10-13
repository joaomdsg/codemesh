import warnings


@deprecated
def price(i):
    return 1 if i.qty > 5 else 2

struct Square
    side::Float64
end

"""
    area(s::Square)

The square's area.
"""
area(s::Square) = _checked(s.side)^2

function _checked(x)
    if x < 0 && !isnan(x)
        throw(DomainError(x))
    end
    x
end

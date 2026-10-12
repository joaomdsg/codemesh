struct Circle
    r::Float64
end

function area(c::Circle)
    pi * _checked(c.r)^2
end

Base.show(io::IO, c::Circle) = print(io, "Circle(", c.r, ")")

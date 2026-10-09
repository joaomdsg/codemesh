"""
    Shapes

Areas of plane shapes.
"""
module Shapes

export Square, Circle, area

include("square.jl")
include("circle.jl")

module Units
export metres
metres(x) = x
end

end

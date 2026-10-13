using Test
using Shapes

const S = Shapes

@testset "Shapes" begin
    @testset "square" begin
        @test area(Square(2.0)) == 4.0
    end
    @testset "units" begin
        @test S.Units.metres(1) == 1
    end
end

pub mod price;

use crate::price::cost;

/// Total of a cart.
pub fn total(n: u32) -> u32 {
    let mut s = 0;
    for i in 0..n {
        if i % 2 == 0 && i > 1 {
            s += cost(i);
        }
    }
    s
}

fn unused() {}

pub struct Cart;

impl Cart {
    pub fn add(&self, i: u32) -> u32 {
        total(i)
    }

    fn reset(&mut self) {}
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn totals() {
        assert_eq!(total(0), 0);
    }
}

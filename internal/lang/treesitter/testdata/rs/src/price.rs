#[deprecated]
pub fn cost(i: u32) -> u32 {
    match i {
        0 => 1,
        _ => 2,
    }
}

use std::fmt::Debug;

import { price } from "./util/price";
import type { Item } from "./item";

/** Total of a cart. */
export function total(items: Item[]): number {
  let sum = 0;
  for (const it of items) {
    if (it.qty > 0 && it.live) {
      sum += price(it);
    }
  }
  return sum;
}

function unused(): void {}

export class Cart {
  add(i: Item): void {
    total([i]);
  }

  private reset() {}
}

export function grade(n: number): string {
  if (n > 90) {
    return "a";
  } else if (n > 80) {
    return "b";
  }
  return "c";
}

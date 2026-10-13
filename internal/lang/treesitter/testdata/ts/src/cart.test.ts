import { total } from "./cart";

test("total", () => {
  expect(total([])).toBe(0);
});

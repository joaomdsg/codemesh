import { Item } from "../item";

/** @deprecated use cost */
export const price = (i: Item) => (i.qty > 5 ? 1 : 2);

export const probe = () => unused();

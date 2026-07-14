import { User, TABLE } from "./models";

export const db = {
  table: TABLE,
  save(u: User) {
    return u;
  },
};

export function connect() {
  return db;
}

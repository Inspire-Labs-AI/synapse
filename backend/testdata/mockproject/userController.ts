import { db } from "./db";
import type { User } from "./models";

export class UserController {
  static list(): User[] {
    return [];
  }

  static create(u: User) {
    return db.save(u);
  }
}

export function getUser(id: string) {
  return id;
}

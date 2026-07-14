import { db } from "./db";
import type { User } from "./models";

export interface Category {
  id: string;
  label: string;
}

export class CategoryController {
  static fetchCategories(): Category[] {
    return [{ id: "1", label: "general" }];
  }

  static assignOwner(c: Category, owner: User) {
    return db.save(owner);
  }
}

export function listCategories(): string[] {
  return CategoryController.fetchCategories().map((c) => c.label);
}

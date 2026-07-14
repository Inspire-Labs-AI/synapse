import express, { Router } from "express";
import { UserController } from "./userController";
import { CategoryController } from "./categoryController";

const router = Router();

router.get("/users", UserController.list);
router.post("/users", UserController.create);
router.get("/category", CategoryController.fetchCategories);

export default router;
export { express };

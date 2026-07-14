import { UserController } from "../../../userController";

export async function GET() {
  return Response.json(UserController.list());
}

export async function POST(req: Request) {
  const body = await req.json();
  return Response.json({ ok: true, body });
}

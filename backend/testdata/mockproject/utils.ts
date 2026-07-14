import _ from "lodash";

export function classNames(...xs: string[]): string {
  return _.compact(xs).join(" ");
}

export const VERSION = "1.0.0";

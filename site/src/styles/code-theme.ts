interface Tokens {
  readonly ground: string;
  readonly surface: string;
  readonly ink: string;
  readonly muted: string;
  readonly border: string;
}

const DARK: Tokens = {
  ground: "#171310",
  surface: "#201b16",
  ink: "#e8e3dc",
  muted: "#948b82",
  border: "#332c25",
};

const LIGHT: Tokens = {
  ground: "#fafaf8",
  surface: "#ffffff",
  ink: "#1c1814",
  muted: "#6e675f",
  border: "#e2ded8",
};

const theme = (name: "dark" | "light", t: Tokens) => ({
  name,
  type: name,
  colors: {
    "editor.background": t.surface,
    "editor.foreground": t.ink,
    "editorLineNumber.foreground": t.muted,
    "editor.selectionBackground": t.border,
    "focusBorder": t.border,
  },
  tokenColors: [
    {
      scope: [
        "comment",
        "entity.name.tag",
        "support.type.property-name",
        "variable.other.assignment",
      ],
      settings: { foreground: t.muted },
    },
  ],
});

export const codeThemes = [theme("dark", DARK), theme("light", LIGHT)];

/*
 * The syntax theme for docs code blocks: two neutrals, and no third colour.
 *
 * Expressive Code's defaults are GitHub's, which set commands in blue and
 * flags in purple. The visual direction names purple and indigo accents on
 * the list of things this site must not look like, and the palette rule is
 * that colour appears exactly once -- when something is ripe. Fifteen YAML
 * blocks of policy reference are not ripe.
 *
 * So the highlighting says the one thing worth saying, and it is the same
 * thing the landing page's terminal block says by hand: keys are `muted`,
 * everything else is `ink`. A reader scanning `configuration.md` for a field
 * name is helped by that distinction and by no other.
 *
 * Two themes rather than one, because `ink` and `muted` are different colours
 * in each. Named `dark` and `light` so Expressive Code's own selectors line up
 * with the attribute the theme toggle writes, and paired with its
 * dark-mode media query so the third state -- no attribute, follow the system
 * -- resolves the same way the rest of the palette does.
 */

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
      // A key, in every language the docs use one: YAML mappings, JSON
      // objects, and the `NAME=` of a shell assignment.
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

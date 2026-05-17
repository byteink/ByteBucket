// Tailwind 4 moved its PostCSS plugin to a separate package; the bare
// "tailwindcss" entry used in Tailwind 3 throws on load. Importing the
// dedicated @tailwindcss/postcss restores the old build pipeline shape.
export default {
  plugins: {
    '@tailwindcss/postcss': {},
    autoprefixer: {},
  },
};

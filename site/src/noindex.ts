/*
 * The pages that are served but not meant to be found.
 *
 * A route here does two things that have to stay in step: it carries
 * `<meta name="robots" content="noindex">` (BaseLayout's `noindex` prop),
 * and it is filtered out of the sitemap below. Listing it in only one of the
 * two is the failure this file exists to make hard -- a noindex page still
 * advertised in the sitemap is the site telling a crawler to fetch a page it
 * is then told to forget.
 *
 * Note what is deliberately absent: a robots.txt `Disallow`. The two
 * mechanisms are not additive. A crawler forbidden to fetch the page never
 * reads the tag, so the Disallow hides the noindex and leaves the URL
 * indexable-by-reference. See site/README.md.
 */
export const NOINDEX_ROUTES: readonly string[] = [
  // The specimen page: served, linked from nowhere, not a page anyone is
  // meant to land on from a search result.
  "/design/",
  // Belt and braces. The sitemap integration drops /404/ on its own; listing
  // it keeps this file readable as the whole set of pages carrying the tag.
  "/404/",
];

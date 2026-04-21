export default {
  async fetch() {
    return Response.redirect(
      "https://raw.githubusercontent.com/olucasandrade/kaptanto/main/scripts/install.sh",
      302
    );
  },
};

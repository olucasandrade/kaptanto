export default {
  async fetch() {
    return Response.redirect(
      "https://raw.githubusercontent.com/kaptanto/kaptanto/main/scripts/install.sh",
      302
    );
  },
};

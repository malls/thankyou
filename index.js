require('dotenv').config();
const express = require('express');
const app = express();
const port = process.env.PORT;

const options = {
  dotfiles: 'ignore',
  etag: false,
  extensions: ['html', 'css', 'js', 'ico'],
  index: false,
  maxAge: '1d',
  redirect: false,
  setHeaders: function (res, path, stat) {
    res.set('x-timestamp', Date.now())
  }
};

app.use(express.static('public', options));


app.get('/', (req, res) => {
  res.redirect('/index.html')
});

app.listen(port, () => {
  console.log(`Example app listening on port ${port}`)
});


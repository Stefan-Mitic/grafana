// import React from 'react';

// const thPadding = { paddingRight: 30 };

export const ConcatenateHelper = () => {
  return `
  This transformation combines all fields from all frames into one result. Consider the following:

**Query A:**

| Temp  | Uptime    |
| ----- | --------- |
| 15.4  | 1230233   |

Query B:

| AQI   | Errors |
| ----- | ------ |
| 3.2   | 5      |

After you concatenate the fields, the data frame would be:

| Temp  | Uptime   | AQI   | Errors |
| ----- | -------- | ----- | ------ |
| 15.4  | 1230233  | 3.2   | 5      |
  `;
};

// <div>
//   <p>This transformation combines all fields from all frames into one result. Consider the following:</p>
//   <p>
//     <strong>Query A:</strong>
//   </p>
//   <table style={{ borderWidth: '1px', borderColor: '#aaaaaa', borderStyle: 'solid' }}>
//     <thead>
//       <tr>
//         <th style={thPadding}>Temp</th>
//         <th style={thPadding}>Uptime</th>
//       </tr>
//     </thead>
//     <tbody>
//       <tr>
//         <td>15.4</td>
//         <td>1230233</td>
//       </tr>
//     </tbody>
//   </table>
//   <br />
//   <br />
//   <p>Query B:</p>
//   <table>
//     <thead>
//       <tr>
//         <th style={thPadding}>AQI</th>
//         <th style={thPadding}>Errors</th>
//       </tr>
//     </thead>
//     <tbody>
//       <tr>
//         <td>3.2</td>
//         <td>5</td>
//       </tr>
//     </tbody>
//   </table>
//   <br />
//   <br />
//   <p>After you concatenate the fields, the data frame would be:</p>
//   <table>
//     <thead>
//       <tr>
//         <th style={thPadding}>Temp</th>
//         <th style={thPadding}>Uptime</th>
//         <th style={thPadding}>AQI</th>
//         <th style={thPadding}>Errors</th>
//       </tr>
//     </thead>
//     <tbody>
//       <tr>
//         <td>15.4</td>
//         <td>1230233</td>
//         <td>3.2</td>
//         <td>5</td>
//       </tr>
//     </tbody>
//   </table>
// </div>
